package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stateFileName = ".gdrive-state.json"

type StateEntry struct {
	Path         string `json:"path"`
	MD5          string `json:"md5,omitempty"`
	ModifiedTime string `json:"modifiedTime,omitempty"`
	Size         int64  `json:"size,omitempty"`
}

type State struct {
	Downloaded map[string]StateEntry `json:"downloaded"`
	Files      []*FileItem           `json:"files,omitempty"`

	path  string
	mu    sync.Mutex
	dirty bool
}

func (s *State) SetFiles(files []*FileItem) {
	s.mu.Lock()
	s.Files = files
	s.dirty = true
	s.mu.Unlock()
}

func LoadState(outputDir string) (*State, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	s := &State{
		Downloaded: make(map[string]StateEntry),
		path:       filepath.Join(outputDir, stateFileName),
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Downloaded == nil {
		s.Downloaded = make(map[string]StateEntry)
	}
	return s, nil
}

func (s *State) Mark(id string, e StateEntry) {
	s.mu.Lock()
	s.Downloaded[id] = e
	s.dirty = true
	s.mu.Unlock()
}

func (s *State) Get(id string) (StateEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.Downloaded[id]
	return e, ok
}

func (s *State) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// AutoFlush flushes the state to disk every interval until ctx is canceled.
func (s *State) AutoFlush(stop <-chan struct{}, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			_ = s.Flush()
			return
		case <-t.C:
			_ = s.Flush()
		}
	}
}
