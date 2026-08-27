package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/YuDong999/opscore/internal/protection"
)

// fileMetaRecord is the metadata line persisted as the FIRST line of the
// durable transition file (P30-I10). It is never a transition; its presence
// lets Load distinguish "no history yet" from "history present but meta lost".
type fileMetaRecord struct {
	Meta        bool  `json:"_meta"`
	FileDropped int64 `json:"file_dropped"`
}

func metaLine(fileDropped int64) string {
	b, _ := json.Marshal(fileMetaRecord{Meta: true, FileDropped: fileDropped})
	return string(b)
}

// FileBackedTransitionStore is the concrete protection.AlertTransitionStore
// (Phase 30, P30-I8 storage boundary). It persists alert transitions as
// newline-delimited JSON in a single file whose FIRST line is a metadata record.
//
// Crash-consistency (P30-I10): normal Append is a pure one-line append (ordered,
// P30-I9). Eviction (only when the file exceeds protection.FileTransitionCapacity)
// rewrites the ENTIRE file via a temp file + atomic os.Rename, so the on-disk
// content and the file_dropped count can NEVER disagree on recovery. If the
// metadata line is missing / unparseable, Load surfaces
// RetentionMetaInconsistent=true honestly (never coerced to 0 / false-clean).
type FileBackedTransitionStore struct {
	path string
	mu   sync.Mutex

	count       int64 // in-memory mirror of on-disk transition-line count
	fileDropped int64 // in-memory mirror of persisted eviction count
}

// NewFileBackedTransitionStore opens (creating and writing the metadata line if
// the file is new) the durable transition file. The parent directory is created
// if needed. It scans the file to mirror count/fileDropped in memory.
func NewFileBackedTransitionStore(path string) (*FileBackedTransitionStore, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.Size() == 0 {
		if _, err := f.WriteString(metaLine(0) + "\n"); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
	}
	f.Close()

	s := &FileBackedTransitionStore{path: path}
	if _, _, _, err := s.scan(); err != nil {
		return nil, err
	}
	return s, nil
}

// scan reads the file and returns (transitions, fileDropped,
// retentionMetaInconsistent, hardErr). It also updates s.count and s.fileDropped
// in memory. Best-effort: an unparseable transition line is skipped (recovered
// around); only a hard I/O error is reported via hardErr (mapped to LoadErr).
func (s *FileBackedTransitionStore) scan() (transitions []protection.AlertTransition, fileDropped int64, metaInconsistent bool, hardErr error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.count = 0
			s.fileDropped = 0
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	raw := strings.TrimRight(string(data), "\n")
	if raw == "" {
		s.count = 0
		s.fileDropped = 0
		return nil, 0, false, nil
	}
	lines := strings.Split(raw, "\n")

	// Line 1 is the metadata record (when we wrote it). If it parses as meta,
	// consume it; otherwise the whole file is treated as transitions and the
	// metadata is marked inconsistent (honest, P30-I10).
	start := 0
	if len(lines) > 0 {
		var m fileMetaRecord
		if json.Unmarshal([]byte(lines[0]), &m) == nil && m.Meta {
			fileDropped = m.FileDropped
			start = 1
		} else {
			metaInconsistent = true // line1 is not a valid meta record
		}
	}

	transitions = make([]protection.AlertTransition, 0, len(lines)-start)
	for _, ln := range lines[start:] {
		if ln == "" {
			continue
		}
		var t protection.AlertTransition
		if err := json.Unmarshal([]byte(ln), &t); err != nil {
			continue // skip unparseable transition line (best-effort recovery)
		}
		// Reject lines that are not genuine transitions: a real edge always
		// carries an observation time. This discards stray/meta-looking lines
		// that happen to be valid JSON but carry no edge (e.g. a corrupted or
		// misplaced meta record).
		if t.At.IsZero() {
			continue
		}
		transitions = append(transitions, t)
	}
	s.count = int64(len(transitions))
	s.fileDropped = fileDropped
	return transitions, fileDropped, metaInconsistent, nil
}

// Load implements protection.AlertTransitionStore.
func (s *FileBackedTransitionStore) Load(ctx context.Context) protection.TransitionLoadResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	txns, fd, inconsistent, err := s.scan()
	if err != nil {
		return protection.TransitionLoadResult{LoadErr: err}
	}
	return protection.TransitionLoadResult{
		Transitions:                txns,
		FileDropped:                fd,
		RetentionMetaInconsistent: inconsistent,
	}
}

// Append implements protection.AlertTransitionStore. It appends one JSON line
// synchronously (P30-I9 ordered) and, when the file exceeds the durable cap,
// rewrites it (temp + atomic rename) keeping the most recent cap transitions and
// incrementing file_dropped (P30-I10). Durability is best-effort (P30-I6): a
// write/evict failure is returned but the caller (Observe) does not block on it.
func (s *FileBackedTransitionStore) Append(ctx context.Context, t protection.AlertTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	line, err := json.Marshal(t)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(string(line) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.count++

	if s.count > protection.FileTransitionCapacity {
		if err := s.evictLocked(); err != nil {
			return err // best-effort; caller ignores, will retry next Append
		}
	}
	return nil
}

// evictLocked rewrites the file to keep only the most recent
// protection.FileTransitionCapacity transition lines, incrementing file_dropped
// by the number evicted. Caller MUST hold s.mu.
func (s *FileBackedTransitionStore) evictLocked() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	raw := strings.TrimRight(string(data), "\n")
	var lines []string
	if raw != "" {
		lines = strings.Split(raw, "\n")
	}
	start := 0
	if len(lines) > 0 {
		var m fileMetaRecord
		if json.Unmarshal([]byte(lines[0]), &m) == nil && m.Meta {
			start = 1
		}
	}
	txLines := lines[start:]
	if len(txLines) <= protection.FileTransitionCapacity {
		return nil // already within cap (e.g. meta was corrupt and counted in)
	}
	evicted := int64(len(txLines) - protection.FileTransitionCapacity)
	keep := txLines[len(txLines)-protection.FileTransitionCapacity:]

	tmp := s.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := tf.WriteString(metaLine(s.fileDropped+evicted) + "\n"); err != nil {
		tf.Close()
		return err
	}
	w := bufio.NewWriter(tf)
	for _, ln := range keep {
		if _, err := w.WriteString(ln + "\n"); err != nil {
			tf.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tf.Close()
		return err
	}
	if err := tf.Sync(); err != nil {
		tf.Close()
		return err
	}
	if err := tf.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.fileDropped += evicted
	s.count = protection.FileTransitionCapacity
	return nil
}

// Close implements protection.AlertTransitionStore. The store opens/closes the
// file per-operation, so there is no persistent handle to release.
func (s *FileBackedTransitionStore) Close() error { return nil }
