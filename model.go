package main

import (
	"fmt"
	"sync"
	"time"
)

type FileStatus int

const (
	StatusQueued FileStatus = iota
	StatusDownloading
	StatusDone
	StatusFailed
	StatusSkipped
)

func (s FileStatus) String() string {
	switch s {
	case StatusQueued:
		return "queued"
	case StatusDownloading:
		return "downloading"
	case StatusDone:
		return "done"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped"
	}
	return "?"
}

type FileItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	Size         int64  `json:"size"`
	RelPath      string `json:"relPath"`
	MD5          string `json:"md5"`
	ModifiedTime string `json:"modifiedTime"`
	IsExport     bool   `json:"isExport"`
	ExportExt    string `json:"exportExt"`

	Status   FileStatus `json:"status"`
	BytesGot int64      `json:"bytesGot"`
	Err      string     `json:"err"`
}

type Phase int

const (
	PhaseIdle Phase = iota
	PhaseAuthenticating
	PhaseScanning
	PhaseDownloading
	PhasePaused
	PhaseDone
	PhaseError
)

func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseAuthenticating:
		return "authenticating"
	case PhaseScanning:
		return "scanning Drive"
	case PhaseDownloading:
		return "downloading"
	case PhasePaused:
		return "paused"
	case PhaseDone:
		return "done"
	case PhaseError:
		return "error"
	}
	return "?"
}

type Model struct {
	mu sync.RWMutex

	CredentialsPath string
	OutputDir       string
	SignedIn        bool
	UserEmail       string

	Phase   Phase
	Message string

	Files   []*FileItem
	byID    map[string]*FileItem
	Total   int
	Done    int
	Failed  int
	Skipped int
	Bytes   int64

	log     []string
	logSize int

	onChange func()
}

func NewModel() *Model {
	return &Model{
		byID:    make(map[string]*FileItem),
		logSize: 200,
	}
}

func (m *Model) SetOnChange(fn func()) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func (m *Model) notify() {
	if m.onChange != nil {
		go m.onChange()
	}
}

func (m *Model) Logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	m.mu.Lock()
	m.log = append(m.log, line)
	if len(m.log) > m.logSize {
		m.log = m.log[len(m.log)-m.logSize:]
	}
	m.mu.Unlock()
	m.notify()
}

func (m *Model) Logs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.log))
	copy(out, m.log)
	return out
}

func (m *Model) SetPhase(p Phase, msg string) {
	m.mu.Lock()
	m.Phase = p
	m.Message = msg
	m.mu.Unlock()
	m.notify()
}

func (m *Model) GetPhase() (Phase, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Phase, m.Message
}

func (m *Model) SetSignedIn(in bool, email string) {
	m.mu.Lock()
	m.SignedIn = in
	m.UserEmail = email
	m.mu.Unlock()
	m.notify()
}

func (m *Model) SetCredentials(p string) {
	m.mu.Lock()
	m.CredentialsPath = p
	m.mu.Unlock()
	m.notify()
}

func (m *Model) SetOutputDir(p string) {
	m.mu.Lock()
	m.OutputDir = p
	m.mu.Unlock()
	m.notify()
}

func (m *Model) ResetFiles(files []*FileItem) {
	m.mu.Lock()
	m.Files = files
	m.byID = make(map[string]*FileItem, len(files))
	for _, f := range files {
		m.byID[f.ID] = f
	}
	m.Total = len(files)
	m.Done = 0
	m.Failed = 0
	m.Skipped = 0
	m.Bytes = 0
	m.mu.Unlock()
	m.notify()
}

func (m *Model) UpdateFile(id string, fn func(*FileItem)) {
	m.mu.Lock()
	if f, ok := m.byID[id]; ok {
		fn(f)
	}
	m.recompute()
	m.mu.Unlock()
	m.notify()
}

func (m *Model) RetryFailed() {
	m.mu.Lock()
	for _, f := range m.Files {
		if f.Status == StatusFailed {
			f.Status = StatusQueued
			f.Err = ""
		}
	}
	m.recompute()
	m.mu.Unlock()
	m.notify()
}

func (m *Model) recompute() {
	done, failed, skipped := 0, 0, 0
	var bytes int64
	for _, f := range m.Files {
		switch f.Status {
		case StatusDone:
			done++
			bytes += f.BytesGot
		case StatusFailed:
			failed++
		case StatusSkipped:
			skipped++
			bytes += f.BytesGot
		case StatusDownloading:
			bytes += f.BytesGot
		}
	}
	m.Done = done
	m.Failed = failed
	m.Skipped = skipped
	m.Bytes = bytes
}

func (m *Model) Snapshot() ModelSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	files := make([]FileItem, len(m.Files))
	for i, f := range m.Files {
		files[i] = *f
	}
	return ModelSnapshot{
		CredentialsPath: m.CredentialsPath,
		OutputDir:       m.OutputDir,
		SignedIn:        m.SignedIn,
		UserEmail:       m.UserEmail,
		Phase:           m.Phase,
		Message:         m.Message,
		Files:           files,
		Total:           m.Total,
		Done:            m.Done,
		Failed:          m.Failed,
		Skipped:         m.Skipped,
		Bytes:           m.Bytes,
	}
}

type ModelSnapshot struct {
	CredentialsPath string
	OutputDir       string
	SignedIn        bool
	UserEmail       string
	Phase           Phase
	Message         string
	Files           []FileItem
	Total           int
	Done            int
	Failed          int
	Skipped         int
	Bytes           int64
}
