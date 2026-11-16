// SPDX-FileCopyrightText: Outwright contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package watch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// State is what an account's watcher has seen, kept across restarts so a restart announces
// nothing old.
type State struct {
	mu          sync.Mutex
	path        string
	UIDValidity uint32 `json:"uidValidity"`
	LastUID     uint32 `json:"lastUid"`
}

// LoadState reads an account's state from dir; a missing file is a fresh start.
func LoadState(dir, name string) (*State, error) {
	sum := sha256.Sum256([]byte(name))
	st := &State{path: filepath.Join(dir, hex.EncodeToString(sum[:8])+".json")}
	b, err := os.ReadFile(st.path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	if len(b) > 4096 || json.Unmarshal(b, st) != nil {
		// A damaged file is a fresh start: nothing old is announced.
		return &State{path: st.path}, nil
	}
	return st, nil
}

// Last is the highest UID already seen.
func (s *State) Last() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastUID
}

func (s *State) get() (validity, last uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.UIDValidity, s.LastUID
}

func (s *State) set(validity, last uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.UIDValidity, s.LastUID = validity, last
}

// Save writes the state atomically; without a path (a test's) it keeps it in memory.
func (s *State) Save() error {
	s.mu.Lock()
	b, err := json.Marshal(s)
	path := s.path
	s.mu.Unlock()
	if err != nil || path == "" {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return nil
}
