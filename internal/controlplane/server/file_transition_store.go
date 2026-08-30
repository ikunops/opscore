package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// openErr captures a hard I/O failure encountered while mirroring the
	// counters during construction (P30-I11-impl). It is NOT reported as a
	// construction error: the store is still handed to the tracker, and Load()
	// surfaces the failure so the read API can expose
	// load_error / available=false instead of silently downgrading the tracker
	// to a clean in-memory one ("unreadable history" must never become
	// "no history").
	openErr error
}

// NewFileBackedTransitionStore opens (creating and writing the metadata line if
// the file is new) the durable transition file. The parent directory is created
// if needed.
//
// P30-I11-impl: OPEN and LOAD are separate lifecycles. This constructor only
// performs SETUP (mkdir / open / write the metadata line) and a best-effort
// counter mirror; content-level failures (unreadable file, corruption) are
// remembered and reported by Load(), never turned into a constructor error.
// Only a genuine setup failure returns an error.
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
	// Best-effort counter mirror (needed so eviction keeps the durable cap).
	// A failure here is remembered, NOT returned: Load() reports it honestly.
	txns, fd, _, _, err := s.scan()
	if err != nil {
		s.openErr = err // P30-I11-impl: degrade through Load, never silently
		return s, nil
	}
	s.count = int64(len(txns))
	s.fileDropped = fd
	return s, nil
}

// scan reads the file and returns (transitions, fileDropped,
// retentionMetaInconsistent, corrupt, hardErr). It also updates s.count and
// s.fileDropped in memory.
//
// P30-I12 recovery rule — ONLY a provably-incomplete trailing write may be
// recovered silently (ADR-053 crash partial-write). "Provably incomplete" means
// the raw file does NOT end with a line terminator. A corrupt line that was
// fully written (terminator present) is NOT a partial write and is reported as
// corruption. Any unparseable line that is not such a trailing write means
// durable history genuinely lost records, so it is signalled instead of being
// `continue`-d over; presenting the surviving remainder as normal history would
// be a new false-clean risk.
func (s *FileBackedTransitionStore) scan() (transitions []protection.AlertTransition, fileDropped int64, metaInconsistent bool, corrupt bool, hardErr error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.count = 0
			s.fileDropped = 0
			return nil, 0, false, false, nil
		}
		return nil, 0, false, false, err
	}
	// P30-I12: a genuine crash partial write is INCOMPLETE — by definition it
	// has NO line terminator. That distinction must be taken from the RAW bytes
	// BEFORE trimming: TrimRight would erase it, making a fully-written but
	// corrupt final line ("<<<CORRUPT>>>\n") indistinguishable from a partial
	// write, and both would be recovered silently. Only when the file does not
	// end with "\n" is the final line provably a partial write.
	raw := string(data)
	partialTrailing := len(raw) > 0 && !strings.HasSuffix(raw, "\n")

	trimmed := strings.TrimRight(raw, "\n")
	if trimmed == "" {
		s.count = 0
		s.fileDropped = 0
		return nil, 0, false, false, nil
	}
	lines := strings.Split(trimmed, "\n")

	// Line 1 is the metadata SLOT. Three cases (P30-I10):
	//   (a) parses as a meta record          -> consume it, metadata consistent.
	//   (b) parses as a genuine transition   -> a legacy / metadata-less file:
	//       keep it as a transition and mark metadata inconsistent.
	//   (c) neither                          -> a CORRUPT metadata slot: consume
	//       it (never re-judge it as a transition line below) and mark metadata
	//       inconsistent. This keeps garbage-meta files recoverable without
	//       misfiring the P30-I12 corruption detector.
	start := 0
	if len(lines) > 0 {
		var m fileMetaRecord
		if json.Unmarshal([]byte(lines[0]), &m) == nil && m.Meta {
			fileDropped = m.FileDropped
			start = 1
		} else {
			var probe protection.AlertTransition
			isTransition := json.Unmarshal([]byte(lines[0]), &probe) == nil && !probe.At.IsZero()
			metaInconsistent = true
			if !isTransition {
				start = 1 // corrupt meta slot: consume, do not treat as transition
			}
		}
	}

	// Collect the non-empty transition lines first, so "trailing" is judged
	// against the LAST NON-EMPTY line (a trailing blank must not mask it).
	txLines := make([]string, 0, len(lines))
	for _, ln := range lines[start:] {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		txLines = append(txLines, ln)
	}

	transitions = make([]protection.AlertTransition, 0, len(txLines))
	last := len(txLines) - 1
	for i, ln := range txLines {
		var t protection.AlertTransition
		if err := json.Unmarshal([]byte(ln), &t); err != nil || t.At.IsZero() {
			if i == last && partialTrailing {
				// ADR-053: the final line carries NO terminator, so it really is
				// an incomplete trailing write — silent recovery is the
				// authorized behavior here, and only here.
				continue
			}
			// P30-I12: corruption mid-history. Stop and signal; never present
			// the remainder as normal history without a corruption marker.
			s.count = int64(len(txLines)) // keep the eviction mirror sane
			return nil, 0, metaInconsistent, true, nil
		}
		transitions = append(transitions, t)
	}
	s.count = int64(len(transitions))
	s.fileDropped = fileDropped
	return transitions, fileDropped, metaInconsistent, false, nil
}

// Load implements protection.AlertTransitionStore.
func (s *FileBackedTransitionStore) Load(ctx context.Context) protection.TransitionLoadResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		// P30-I11-impl: the durable file could not be read at all. Report it
		// honestly instead of letting the caller fall back to a clean
		// in-memory tracker (which would turn "unreadable history" into
		// "no history").
		return protection.TransitionLoadResult{LoadErr: s.openErr}
	}
	txns, fd, inconsistent, corrupt, err := s.scan()
	if err != nil {
		return protection.TransitionLoadResult{LoadErr: err}
	}
	return protection.TransitionLoadResult{
		Transitions:                txns,
		FileDropped:                fd,
		RetentionMetaInconsistent: inconsistent,
		Corrupt:                    corrupt, // P30-I12
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

// durableReadMaxBytes is the durable-read byte budget (P31-I3). It is enforced
// via Stat BEFORE any read: a budget that only applies after the file has been
// pulled into memory is not a budget at all.
const durableReadMaxBytes = 8 << 20 // 8 MiB

// ErrDurableBudgetExceeded is returned when the durable transition file exceeds
// the read budget. It is a sentinel so the handler can report the specific
// reason instead of a generic error (P31-I4 honesty).
var ErrDurableBudgetExceeded = errors.New("durable transition read budget exceeded")

// ReadRecent implements protection.AlertTransitionStore (Phase 31). It performs
// a BOUNDED durable read projection: up to n persisted transitions returned
// NEWEST-FIRST. It is NOT a replay and NEVER re-evaluates the alert (P31-I2).
//
// Bounds (P31-I3), each enforced BEFORE the work it limits:
//   - ctx is honoured first (the handler applies a 2s deadline)
//   - n is clamped to [1, min(DurableReadMaxLimit, FileTransitionCapacity)]
//   - the file size is checked against durableReadMaxBytes via Stat, before reading
//
// Corruption semantics come from the SAME scan() used by startup Load (P31-I5):
// there is exactly ONE definition of "corrupt" for the durable file, so a
// startup Load and a deep read can never disagree about the same bytes.
//
// The result is taken from the durable dataset directly and is NEVER routed
// through the 256-entry runtime ring — doing so would silently degrade this
// read surface back to the Phase 29 cap (R141 implementation constraint).
func (s *FileBackedTransitionStore) ReadRecent(ctx context.Context, n int) protection.TransitionReadResult {
	if err := ctx.Err(); err != nil {
		return protection.TransitionReadResult{LoadErr: err}
	}
	if n <= 0 {
		return protection.TransitionReadResult{}
	}
	if n > protection.DurableReadMaxLimit {
		n = protection.DurableReadMaxLimit
	}
	if int64(n) > protection.FileTransitionCapacity {
		n = int(protection.FileTransitionCapacity)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openErr != nil {
		return protection.TransitionReadResult{LoadErr: s.openErr}
	}
	// P31-I10: a Stat failure must NOT be swallowed. With the previous
	// `if st, err := os.Stat(...); err == nil && size > budget` form, a Stat
	// error silently skipped the budget check and fell through to scan()/
	// ReadFile — i.e. the budget was bypassed exactly when the file was in an
	// abnormal state. If the file cannot even be stat-ed, the read has already
	// failed; report it explicitly.
	st, err := os.Stat(s.path)
	if err != nil {
		return protection.TransitionReadResult{
			LoadErr: fmt.Errorf("durable read stat: %w", err),
		}
	}
	if st.Size() > durableReadMaxBytes {
		return protection.TransitionReadResult{
			LoadErr: fmt.Errorf("%w: %d bytes > %d", ErrDurableBudgetExceeded, st.Size(), durableReadMaxBytes),
		}
	}
	if err := ctx.Err(); err != nil {
		return protection.TransitionReadResult{LoadErr: err}
	}

	txns, fd, metaInc, corrupt, err := s.scan()
	if err != nil {
		return protection.TransitionReadResult{LoadErr: err}
	}
	if corrupt {
		// Corrupt durable history is never served as authoritative data.
		return protection.TransitionReadResult{Corrupt: true}
	}

	// Take the newest n from the durable dataset, then reverse to NEWEST-FIRST.
	start := 0
	if len(txns) > n {
		start = len(txns) - n
	}
	sel := txns[start:]
	out := make([]protection.AlertTransition, 0, len(sel))
	for i := len(sel) - 1; i >= 0; i-- {
		out = append(out, sel[i])
	}
	return protection.TransitionReadResult{
		Transitions:                out,
		FileDropped:                fd,
		RetentionMetaInconsistent: metaInc,
	}
}

// FailedTransitionStore is a DEGRADED protection.AlertTransitionStore used when
// the durable transition file could not be opened at all (P30-I11-impl).
//
// It exists so the wiring can hand the tracker a NON-NIL store that reports the
// failure: the tracker then exposes load_error=true / available=false over the
// read API. Without it, the wiring would fall back to a clean in-memory
// tracker and an unreadable persisted history would become indistinguishable
// from "there is no history" — exactly the false-clean P30-I11 forbids.
type FailedTransitionStore struct {
	path string
	err  error
}

// NewFailedTransitionStore builds a degraded store that reports err from every
// operation. It NEVER returns nil, so the tracker stays honestly degraded.
func NewFailedTransitionStore(path string, err error) *FailedTransitionStore {
	return &FailedTransitionStore{path: path, err: err}
}

// Append reports the failure (P30-I6 best-effort): durability is unavailable,
// but the caller keeps serving on the live ring.
func (s *FailedTransitionStore) Append(ctx context.Context, t protection.AlertTransition) error {
	return s.err
}

// Load always reports the failure, so the tracker marks history unavailable
// instead of pretending the durable history was empty (P30-I11).
func (s *FailedTransitionStore) Load(ctx context.Context) protection.TransitionLoadResult {
	return protection.TransitionLoadResult{LoadErr: s.err}
}

// ReadRecent reports the same failure (P31-I9): a degraded store must never
// look like "durable history exists but is empty".
func (s *FailedTransitionStore) ReadRecent(ctx context.Context, n int) protection.TransitionReadResult {
	return protection.TransitionReadResult{LoadErr: s.err}
}

// Close implements protection.AlertTransitionStore.
func (s *FailedTransitionStore) Close() error { return nil }
