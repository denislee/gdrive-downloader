package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const folderMime = "application/vnd.google-apps.folder"

// exportTable maps Google-native MIME types to the export MIME + file extension.
var exportTable = map[string]struct {
	Mime string
	Ext  string
}{
	"application/vnd.google-apps.document":     {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	"application/vnd.google-apps.spreadsheet":  {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
	"application/vnd.google-apps.presentation": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
	"application/vnd.google-apps.drawing":      {"image/png", ".png"},
	"application/vnd.google-apps.script":       {"application/vnd.google-apps.script+json", ".json"},
}

type Driver struct {
	svc   *drive.Service
	model *Model
	state *State
}

func NewDriver(ctx context.Context, client *http.Client, model *Model, state *State) (*Driver, error) {
	svc, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	return &Driver{svc: svc, model: model, state: state}, nil
}

// Scan lists all non-trashed files and folders in My Drive (corpora=user) and
// builds the FileItem list with mirrored relative paths.
func (d *Driver) Scan(ctx context.Context) ([]*FileItem, error) {
	type raw struct {
		ID        string
		Name      string
		MimeType  string
		Size      int64
		Parents   []string
		MD5       string
		Modified  string
		Shortcut  *drive.FileShortcutDetails
	}

	var all []raw
	pageToken := ""
	for {
		call := d.svc.Files.List().
			Context(ctx).
			Corpora("user").
			IncludeItemsFromAllDrives(false).
			SupportsAllDrives(false).
			PageSize(1000).
			Q("trashed = false").
			Fields("nextPageToken, files(id, name, mimeType, size, parents, md5Checksum, modifiedTime, shortcutDetails)")
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		for _, f := range resp.Files {
			all = append(all, raw{
				ID: f.Id, Name: f.Name, MimeType: f.MimeType, Size: f.Size,
				Parents: f.Parents, MD5: f.Md5Checksum, Modified: f.ModifiedTime,
				Shortcut: f.ShortcutDetails,
			})
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	// Sort for deterministic collision handling.
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })

	// Build folder index.
	folders := map[string]raw{}
	for _, r := range all {
		if r.MimeType == folderMime {
			folders[r.ID] = r
		}
	}

	usedPaths := make(map[string]bool)
	makeUnique := func(dir, name string) string {
		rel := filepath.Join(dir, name)
		if !usedPaths[rel] {
			usedPaths[rel] = true
			return rel
		}
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		for i := 1; ; i++ {
			newName := fmt.Sprintf("%s (%d)%s", stem, i, ext)
			rel = filepath.Join(dir, newName)
			if !usedPaths[rel] {
				usedPaths[rel] = true
				return rel
			}
		}
	}

	pathCache := map[string]string{}
	var pathOf func(id string) string
	pathOf = func(id string) string {
		if id == "" {
			return ""
		}
		if v, ok := pathCache[id]; ok {
			return v
		}
		f, ok := folders[id]
		if !ok {
			pathCache[id] = ""
			return ""
		}
		var parent string
		if len(f.Parents) > 0 {
			parent = pathOf(f.Parents[0])
		}
		p := makeUnique(parent, sanitize(f.Name))
		pathCache[id] = p
		return p
	}

	var items []*FileItem
	for _, r := range all {
		if r.MimeType == folderMime {
			continue
		}
		if r.MimeType == "application/vnd.google-apps.shortcut" {
			continue
		}
		var dir string
		if len(r.Parents) > 0 {
			dir = pathOf(r.Parents[0])
		}
		name := sanitize(r.Name)
		var isExport bool
		var ext string
		if exp, ok := exportTable[r.MimeType]; ok {
			isExport = true
			ext = exp.Ext
			if !strings.HasSuffix(strings.ToLower(name), ext) {
				name += ext
			}
		} else if strings.HasPrefix(r.MimeType, "application/vnd.google-apps.") {
			// Unsupported native type — skip with log.
			d.model.Logf("skipping %s (unsupported Google type %s)", r.Name, r.MimeType)
			continue
		}
		rel := makeUnique(dir, name)
		items = append(items, &FileItem{
			ID:           r.ID,
			Name:         r.Name,
			MimeType:     r.MimeType,
			Size:         r.Size,
			RelPath:      rel,
			MD5:          r.MD5,
			ModifiedTime: r.Modified,
			IsExport:     isExport,
			ExportExt:    ext,
			Status:       StatusQueued,
		})
	}
	return items, nil
}

// Run starts the worker pool and downloads all queued files. It returns when
// either ctx is cancelled or every file has reached a terminal status.
func (d *Driver) Run(ctx context.Context, outputDir string) {
	workers := runtime.NumCPU()
	if workers > 6 {
		workers = 6
	}
	if workers < 2 {
		workers = 2
	}

	jobs := make(chan *FileItem)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				d.handleOne(ctx, outputDir, f)
			}
		}()
	}

	snap := d.model.Snapshot()
	files := snap.Files
	go func() {
		defer close(jobs)
		for i := range files {
			id := files[i].ID
			d.model.mu.RLock()
			fi := d.model.byID[id]
			d.model.mu.RUnlock()
			if fi == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- fi:
			}
		}
	}()
	wg.Wait()
}

func (d *Driver) handleOne(ctx context.Context, outputDir string, f *FileItem) {
	if ctx.Err() != nil {
		return
	}

	// Resume: skip if state matches.
	if e, ok := d.state.Get(f.ID); ok {
		if e.MD5 != "" && f.MD5 != "" && e.MD5 == f.MD5 {
			d.markSkipped(f, "already downloaded")
			return
		}
		if e.MD5 == "" && e.ModifiedTime == f.ModifiedTime && e.ModifiedTime != "" {
			d.markSkipped(f, "already exported")
			return
		}
	}

	dest := filepath.Join(outputDir, f.RelPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		d.markFailed(f, err)
		return
	}

	d.model.UpdateFile(f.ID, func(fi *FileItem) {
		fi.Status = StatusDownloading
		fi.BytesGot = 0
		fi.Err = ""
	})

	tmp := dest + ".part"
	var offset int64
	if !f.IsExport {
		if st, err := os.Stat(tmp); err == nil {
			offset = st.Size()
		}
	}

	// Use parallel download for new large files.
	// We only do this for new files (offset == 0) to avoid complex hole-tracking logic.
	if offset == 0 && f.Size > 20*1024*1024 && !f.IsExport {
		if err := d.downloadParallel(ctx, f, tmp); err != nil {
			_ = os.Remove(tmp)
			d.markFailed(f, err)
			return
		}
	} else {
		if err := d.downloadSequential(ctx, f, tmp, offset); err != nil {
			// Don't remove tmp if it was a partial download we can resume later,
			// unless it's an export (which we can't resume).
			if f.IsExport {
				_ = os.Remove(tmp)
			}
			d.markFailed(f, err)
			return
		}
	}

	if err := os.Rename(tmp, dest); err != nil {
		d.markFailed(f, err)
		return
	}

	snap := d.model.Snapshot()
	if snap.DeleteAfterDownload {
		if err := d.verifyAndDelete(f, dest); err != nil {
			d.model.Logf("VERIFY/DELETE FAILED %s: %s", f.Name, err)
			// We don't mark as failed because the download itself was successful.
		}
	}

	d.state.Mark(f.ID, StateEntry{
		Path:         f.RelPath,
		MD5:          f.MD5,
		ModifiedTime: f.ModifiedTime,
		Size:         f.Size,
	})
	d.model.UpdateFile(f.ID, func(fi *FileItem) {
		fi.Status = StatusDone
	})
}

func (d *Driver) verifyAndDelete(f *FileItem, localPath string) error {
	// Verify size.
	st, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if !f.IsExport && st.Size() != f.Size {
		return fmt.Errorf("size mismatch: drive %d, local %d", f.Size, st.Size())
	}

	// Verify MD5 (only for non-exports, Google doesn't provide MD5 for exports).
	if !f.IsExport && f.MD5 != "" {
		hash := md5.New()
		fh, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("open: %w", err)
		}
		defer fh.Close()
		if _, err := io.Copy(hash, fh); err != nil {
			return fmt.Errorf("hash: %w", err)
		}
		localMD5 := hex.EncodeToString(hash.Sum(nil))
		if localMD5 != f.MD5 {
			return fmt.Errorf("MD5 mismatch: drive %s, local %s", f.MD5, localMD5)
		}
	}

	// Delete from Google Drive.
	d.model.Logf("VERIFIED %s, deleting from Drive...", f.Name)
	if err := d.svc.Files.Delete(f.ID).Do(); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	d.model.Logf("DELETED %s from Drive", f.Name)
	return nil
}

func (d *Driver) downloadSequential(ctx context.Context, f *FileItem, tmp string, offset int64) error {
	var out *os.File
	if offset > 0 {
		var err error
		out, err = os.OpenFile(tmp, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		d.model.UpdateFile(f.ID, func(fi *FileItem) { fi.BytesGot = offset })
	} else {
		var err error
		out, err = os.Create(tmp)
		if err != nil {
			return err
		}
	}
	defer out.Close()

	body, err := d.openBody(ctx, f, offset, 0)
	if err != nil {
		return err
	}
	defer body.Close()

	cw := &countingWriter{w: out, onWrite: func(n int) {
		d.model.UpdateFile(f.ID, func(fi *FileItem) { fi.BytesGot += int64(n) })
	}}
	_, err = io.Copy(cw, body)
	return err
}

func (d *Driver) downloadParallel(ctx context.Context, f *FileItem, tmp string) error {
	const chunkSize = 8 * 1024 * 1024 // 8MB
	const maxParallel = 4

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := out.Truncate(f.Size); err != nil {
		return err
	}

	type chunk struct {
		start, end int64
	}
	chunks := make(chan chunk)
	go func() {
		defer close(chunks)
		for start := int64(0); start < f.Size; start += chunkSize {
			end := start + chunkSize - 1
			if end >= f.Size {
				end = f.Size - 1
			}
			select {
			case <-ctx.Done():
				return
			case chunks <- chunk{start, end}:
			}
		}
	}()

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < maxParallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 32*1024)
			for c := range chunks {
				if ctx.Err() != nil {
					return
				}
				body, err := d.openBody(ctx, f, c.start, c.end)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}

				curr := c.start
				for {
					n, rerr := body.Read(buf)
					if n > 0 {
						if _, werr := out.WriteAt(buf[:n], curr); werr != nil {
							_ = body.Close()
							select {
							case errCh <- werr:
							default:
							}
							return
						}
						curr += int64(n)
						d.model.UpdateFile(f.ID, func(fi *FileItem) { fi.BytesGot += int64(n) })
					}
					if rerr != nil {
						_ = body.Close()
						if rerr != io.EOF {
							select {
							case errCh <- rerr:
							default:
							}
						}
						break
					}
					if ctx.Err() != nil {
						_ = body.Close()
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return ctx.Err()
	}
}

func (d *Driver) openBody(ctx context.Context, f *FileItem, start, end int64) (io.ReadCloser, error) {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		var resp *http.Response
		var err error
		if f.IsExport {
			// Exports don't support Range.
			exp := exportTable[f.MimeType]
			resp, err = d.svc.Files.Export(f.ID, exp.Mime).Context(ctx).Download()
		} else {
			call := d.svc.Files.Get(f.ID)
			if start > 0 || end > 0 {
				rangeHeader := fmt.Sprintf("bytes=%d-", start)
				if end > 0 {
					rangeHeader = fmt.Sprintf("bytes=%d-%d", start, end)
				}
				call.Header().Set("Range", rangeHeader)
			}
			resp, err = call.Context(ctx).Download()
		}
		if err == nil {
			return resp.Body, nil
		}
		lastErr = err
		if !shouldRetry(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after retries: %w", lastErr)
}

func shouldRetry(err error) bool {
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		if ge.Code == 429 || (ge.Code >= 500 && ge.Code < 600) {
			return true
		}
		return false
	}
	return true
}

func (d *Driver) markFailed(f *FileItem, err error) {
	msg := err.Error()
	d.model.UpdateFile(f.ID, func(fi *FileItem) {
		fi.Status = StatusFailed
		fi.Err = msg
	})
	d.model.Logf("FAIL %s: %s", f.Name, msg)
}

func (d *Driver) markSkipped(f *FileItem, reason string) {
	d.model.UpdateFile(f.ID, func(fi *FileItem) {
		fi.Status = StatusSkipped
	})
	_ = reason
}

type countingWriter struct {
	w       io.Writer
	onWrite func(n int)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 && c.onWrite != nil {
		c.onWrite(n)
	}
	return n, err
}

// sanitize replaces filesystem-unfriendly characters in a Drive item name.
func sanitize(name string) string {
	if name == "" {
		return "untitled"
	}
	bad := []rune{'/', '\\', ':', '*', '?', '"', '<', '>', '|', '\x00'}
	out := []rune(name)
	for i, r := range out {
		for _, b := range bad {
			if r == b {
				out[i] = '_'
				break
			}
		}
	}
	s := strings.TrimSpace(string(out))
	s = strings.Trim(s, ".")
	if s == "" {
		return "untitled"
	}
	return s
}
