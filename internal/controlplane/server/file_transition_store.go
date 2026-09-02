package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// fileMetaRecord is the metadata line persisted as the FIRST line of the
// durable transition file (P30-I10). It is never a transition; its presence
// lets Load distinguish "no history yet" from "history present but meta lost".
//
// Phase 32 extends it with LastSeq (P32-I13) and MetaInconsistent.
// MetaInconsistent exists so a legacy-format migration can NEVER launder a
// known-unknown: if the pre-migration metadata was unparseable, file_dropped is
// genuinely unknown, and writing 0 without a marker would be false-clean. The
// flag carries that "unknown" across the migration honestly.
type fileMetaRecord struct {
	Meta             bool  `json:"_meta"`
	FileDropped      int64 `json:"file_dropped"`
	LastSeq          int64 `json:"last_seq"`
	MetaInconsistent bool  `json:"meta_inconsistent,omitempty"`
}

func metaLine(fileDropped, lastSeq int64, metaInconsistent bool) string {
	b, _ := json.Marshal(fileMetaRecord{
		Meta:             true,
		FileDropped:      fileDropped,
		LastSeq:          lastSeq,
		MetaInconsistent: metaInconsistent,
	})
	return string(b)
}

// persistedTransition is the Phase 32 durable record ENVELOPE (P32-I8 revision).
//
// Seq is the STABLE DURABLE IDENTITY (P32-I13): monotonic, never reused, carried
// by the record itself. It is therefore immune to everything that made content
// hashes and relative occurrences unstable:
//
//	eviction rewrite moving offsets  -> irrelevant (Seq travels with the record)
//	older duplicate being evicted    -> irrelevant (Seq is absolute, not ordinal)
//	timestamp collision              -> irrelevant (At is not the identity)
//	content-identical duplicates     -> irrelevant (Seq is unique by allocation)
//
// The envelope carries EXPLICIT json tags, so the on-disk contract no longer
// depends on Go field names (a latent fragility inherited from Phase 30, where
// the record was marshalled straight from AlertTransition).
//
// protection.AlertTransition is deliberately unchanged: Seq is a storage-layer
// concern and never enters the API response (P32-I5 preserved).
type persistedTransition struct {
	Seq         int64     `json:"seq"`
	At          time.Time `json:"at"`
	From        bool      `json:"from"`
	To          bool      `json:"to"`
	UnknownRate int64     `json:"unknown_rate"`
	Threshold   int64     `json:"threshold"`
}

func newPersisted(seq int64, t protection.AlertTransition) persistedTransition {
	return persistedTransition{
		Seq:         seq,
		At:          t.At,
		From:        t.From,
		To:          t.To,
		UnknownRate: t.UnknownRate,
		Threshold:   t.Threshold,
	}
}

func (p persistedTransition) transition() protection.AlertTransition {
	return protection.AlertTransition{
		At:          p.At,
		From:        p.From,
		To:          p.To,
		UnknownRate: p.UnknownRate,
		Threshold:   p.Threshold,
	}
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
	lastSeq     int64 // P32-I13: highest sequence ever allocated (never reused)

	// closeFn closes the append file handle inside Append. It exists solely so
	// tests can simulate a post-sync close failure — the exact P32-I15 hazard
	// where the record is already durable but a later step reports an error.
	// Production always uses (*os.File).Close.
	closeFn func(*os.File) error

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
// performs SETUP (mkdir / open / write the metadata line), a best-effort counter
// mirror, and the one-shot legacy migration; content-level failures (unreadable
// file, corruption, failed migration) are remembered and reported by Load(),
// never turned into a constructor error. Only a genuine setup failure returns an
// error.
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
		if _, err := f.WriteString(metaLine(0, 0, false) + "\n"); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
	}
	f.Close()

	s := &FileBackedTransitionStore{path: path, closeFn: (*os.File).Close}
	recs, fd, metaSeq, metaInc, corrupt, legacy, err := s.scanRecords()
	if err != nil {
		s.openErr = err // P30-I11-impl: degrade through Load, never silently
		return s, nil
	}
	if corrupt {
		// P30-I12: a corrupt file is NOT migrated. Migrating it would launder
		// known-lost records into a clean-looking v2 sequence space.
		s.count = int64(len(recs))
		s.fileDropped = fd
		return s, nil
	}
	if legacy {
		// P32-I14: migration must publish a COMPLETE v2 state or leave the store
		// degraded. It runs here, before the store can serve anything, so a
		// client can never observe a partially migrated sequence space.
		if err := s.migrateLocked(fd, metaInc); err != nil {
			s.openErr = fmt.Errorf("legacy transition migration: %w", err) // degraded, never partial
			return s, nil
		}
		// Re-scan so every in-memory mirror reflects the published v2 state.
		recs, fd, metaSeq, metaInc, corrupt, legacy, err = s.scanRecords()
		if err != nil || corrupt || legacy {
			s.openErr = fmt.Errorf("post-migration verification failed (err=%v corrupt=%v legacy=%v)", err, corrupt, legacy)
			return s, nil
		}
	}
	s.count = int64(len(recs))
	s.fileDropped = fd
	// P32-I15 (R150=B): recovery MUST combine BOTH sources —
	//
	//	lastSeq = max(meta.last_seq, max(record.seq))
	//
	// Taking only max(record.seq) silently drops the persisted watermark, so a
	// sequence that was already allocated (and whose record may simply have been
	// evicted, or lost to a torn write) would be handed out again. That is
	// reuse, which P32-I13/I15 forbid: gaps are allowed, reuse is not.
	s.lastSeq = metaSeq
	for _, r := range recs {
		if r.seq > s.lastSeq {
			s.lastSeq = r.seq
		}
	}
	_ = metaInc
	return s, nil
}

// migrateLocked upgrades a legacy (pre-seq) durable file to v2 in one atomic
// step: it assigns sequences 1..N following the EXISTING durable order, then
// publishes the whole file via temp + rename.
//
// P32-I14 (Migration Atomic Visibility): the rewrite is atomic, so a reader
// observes either the complete legacy file or the complete v2 file — never a
// half-assigned sequence space. Caller MUST hold s.mu.
func (s *FileBackedTransitionStore) migrateLocked(fileDropped int64, metaInconsistent bool) error {
	recs, _, _, _, corrupt, _, err := s.scanRecords()
	if err != nil {
		return err
	}
	if corrupt {
		return errors.New("refusing to migrate a corrupt legacy file")
	}
	tmp := s.path + ".migrate.tmp"
	tf, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// The pre-migration metadata was unparseable, so file_dropped is genuinely
	// unknown; carry that forward instead of writing a clean-looking 0.
	fd := fileDropped
	if metaInconsistent {
		fd = 0
	}
	lastSeq := int64(len(recs))
	if _, err := tf.WriteString(metaLine(fd, lastSeq, metaInconsistent) + "\n"); err != nil {
		tf.Close()
		return err
	}
	w := bufio.NewWriter(tf)
	for i := range recs {
		line, err := json.Marshal(newPersisted(int64(i+1), recs[i].rec))
		if err != nil {
			tf.Close()
			return err
		}
		if _, err := w.WriteString(string(line) + "\n"); err != nil {
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
	return nil
}

// scan reads the file and returns (transitions, fileDropped,
// retentionMetaInconsistent, corrupt, hardErr).
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
	recs, fd, _, mi, corrupt, _, err := s.scanRecords()
	if err != nil {
		return nil, 0, mi, false, err
	}
	if corrupt {
		return nil, 0, mi, true, nil
	}
	out := make([]protection.AlertTransition, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.rec)
	}
	return out, fd, mi, false, nil
}

// scannedRecord pairs a parsed transition with its durable sequence (P32-I13).
type scannedRecord struct {
	rec protection.AlertTransition
	seq int64
}

// scanRecords is the Phase 32 generalised scan: it returns each parsed
// transition together with its stable durable sequence, the persisted sequence
// watermark from the metadata record (P32-I15), and whether the file is still in
// the LEGACY (pre-seq) format.
//
// metaLastSeq MUST be surfaced to callers: the recovery rule is
// lastSeq = max(meta.last_seq, max(record.seq)), and dropping the persisted
// watermark lets an already-allocated sequence be handed out again (R150=B).
//
// Corruption semantics are exactly those of P30-I12 (same trailing/middle
// discrimination), so startup Load and the paged durable read can never disagree.
func (s *FileBackedTransitionStore) scanRecords() (records []scannedRecord, fileDropped, metaLastSeq int64, metaInconsistent bool, corrupt bool, legacy bool, hardErr error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.count = 0
			s.fileDropped = 0
			return nil, 0, 0, false, false, false, nil
		}
		return nil, 0, 0, false, false, false, err
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
		return nil, 0, 0, false, false, false, nil
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
			metaLastSeq = m.LastSeq // P32-I15: the persisted watermark
			if m.MetaInconsistent {
				metaInconsistent = true
			}
			start = 1
		} else {
			var probe persistedTransition
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

	records = make([]scannedRecord, 0, len(txLines))
	last := len(txLines) - 1
	for i, ln := range txLines {
		var p persistedTransition
		if err := json.Unmarshal([]byte(ln), &p); err != nil || p.At.IsZero() {
			if i == last && partialTrailing {
				// ADR-053: the final line carries NO terminator, so it really is
				// an incomplete trailing write — silent recovery is the
				// authorized behavior here, and only here.
				continue
			}
			// P30-I12: corruption mid-history. Stop and signal; never present
			// the remainder as normal history without a corruption marker.
			s.count = int64(len(txLines)) // keep the eviction mirror sane
			return nil, 0, metaLastSeq, metaInconsistent, true, false, nil
		}
		// P32: a record without a sequence was written by the pre-Phase-32
		// format (AlertTransition had no seq and no json tags).
		if p.Seq == 0 {
			legacy = true
		}
		records = append(records, scannedRecord{rec: p.transition(), seq: p.Seq})
	}
	s.count = int64(len(records))
	s.fileDropped = fileDropped
	return records, fileDropped, metaLastSeq, metaInconsistent, false, legacy, nil
}

// Load implements protection.AlertTransitionStore.
func (s *FileBackedTransitionStore) Load(ctx context.Context) protection.TransitionLoadResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		// P30-I11-impl: the durable file could not be read at all (or its legacy
		// migration failed). Report it honestly instead of letting the caller
		// fall back to a clean in-memory tracker (which would turn an
		// unreadable persisted history into "no history").
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
		Corrupt:                    corrupt,
	}
}

// Append implements protection.AlertTransitionStore. It appends one JSON line
// synchronously (P30-I9 ordered) and, when the file exceeds the durable cap,
// rewrites it (temp + atomic rename) keeping the most recent cap transitions and
// incrementing file_dropped (P30-I10). Durability is best-effort (P30-I6): a
// write/evict failure is returned but the caller (Observe) does not block on it.
//
// P32-I15: each appended record receives a fresh, never-reused sequence.
func (s *FileBackedTransitionStore) Append(ctx context.Context, t protection.AlertTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openErr != nil {
		return s.openErr
	}
	seq := s.lastSeq + 1
	line, err := json.Marshal(newPersisted(seq, t))
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
	// P32-I15: the record is DURABLE at this point, so this sequence is
	// CONSUMED — advance the watermark BEFORE any subsequent step can fail.
	//
	// Previously the watermark advanced only after a successful f.Close(), so a
	// post-sync close error returned early and left lastSeq behind: the record
	// was already on disk while lastSeq still pointed below it, and the next
	// Append would re-issue the SAME sequence. That is sequence reuse, which
	// P32-I13/I15 forbid outright. From here on, later errors may change this
	// call's return value but can never make a consumed sequence allocatable
	// again.
	s.lastSeq = seq
	s.count++

	closeErr := s.closeFn(f)

	var evictErr error
	if s.count > protection.FileTransitionCapacity {
		evictErr = s.evictLocked() // best-effort; caller ignores, retried later
	}
	if closeErr != nil {
		return closeErr
	}
	return evictErr
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
	metaInc := false
	var meta fileMetaRecord
	if len(lines) > 0 {
		if json.Unmarshal([]byte(lines[0]), &meta) == nil && meta.Meta {
			start = 1
			metaInc = meta.MetaInconsistent
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
	fd := s.fileDropped
	if metaInc {
		fd = 0
	}
	if _, err := tf.WriteString(metaLine(fd+evicted, s.lastSeq, metaInc) + "\n"); err != nil {
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

// ---------------------------------------------------------------------------
// Phase 32 — opaque cursor over the stable durable sequence
// ---------------------------------------------------------------------------
//
// Cursor: base64url("v2:" + seq)
//
// The identity is the record's durable SEQUENCE (P32-I13), not its content, not
// its byte offset and not its timestamp. That is what makes it immune to all
// four instabilities identified during review:
//
//	eviction rewrite moving offsets  -> Seq travels with the record
//	older duplicate being evicted    -> Seq is absolute, never re-numbered
//	timestamp collision              -> At is not the identity
//	content-identical duplicates     -> Seq is unique by allocation
//
// P32-I16 (Cursor Version Exclusivity): only "v2:" is accepted. A v1
// (fingerprint+ordinal) token is an obsolete protocol version with a proven
// drift defect, so it is rejected as 400 invalid_cursor — it is NOT an expired
// record (410) and it is never re-interpreted.

const cursorVersion = "v2"

// mintCursor renders the opaque token for a durable sequence.
func mintCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorVersion + ":" + strconv.FormatInt(seq, 10)))
}

// parseCursor decodes an opaque cursor and returns the durable sequence it
// identifies. Any other version or malformed token is ErrInvalidCursor.
func parseCursor(cur string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return 0, fmt.Errorf("%w: undecodable", protection.ErrInvalidCursor)
	}
	s := string(raw)
	prefix := cursorVersion + ":"
	if !strings.HasPrefix(s, prefix) {
		// P32-I16: v1 (or anything else) is an unsupported protocol version.
		return 0, fmt.Errorf("%w: unsupported cursor version", protection.ErrInvalidCursor)
	}
	seq, err := strconv.ParseInt(strings.TrimPrefix(s, prefix), 10, 64)
	if err != nil || seq <= 0 {
		return 0, fmt.Errorf("%w: bad sequence", protection.ErrInvalidCursor)
	}
	return seq, nil
}

// resolveCursor locates the record carrying the cursor's durable sequence.
//
// P32-I11: the returned INDEX comes from the durable order; the sequence only
// identifies the record and never participates in ordering.
// P32-I12: because the identity is absolute, neither a rewrite nor the eviction
// of older records (including older duplicates) can invalidate the cursor —
// only the cursor's own record leaving retention does.
func resolveCursor(records []scannedRecord, seq int64) (int, error) {
	for i := range records {
		if records[i].seq == seq {
			return i, nil
		}
	}
	return -1, fmt.Errorf("%w: sequence %d no longer retained", protection.ErrCursorExpired, seq)
}

// ReadRecent implements protection.AlertTransitionStore (Phase 31). It performs
// a BOUNDED durable read projection: up to n persisted transitions returned
// NEWEST-FIRST. It is NOT a replay and NEVER re-evaluates the alert (P31-I2).
//
// Bounds (P31-I3), each enforced before the work it limits:
//   - ctx is honoured first (the handler applies a 2s deadline; long parses abort)
//   - n is clamped to [1, min(DurableReadMaxLimit, FileTransitionCapacity)]
//   - the file size is checked via Stat against an 8 MiB budget BEFORE reading
//
// Corruption semantics come from the SAME scan() used by startup Load (P31-I5):
// there is exactly ONE definition of "corrupt" for the durable file, so a
// startup Load and a deep read can never disagree about the same bytes.
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

	recs, fd, _, metaInc, corrupt, _, err := s.scanRecords()
	if err != nil {
		return protection.TransitionReadResult{LoadErr: err}
	}
	if corrupt {
		// Corrupt durable history is never served as authoritative data.
		return protection.TransitionReadResult{Corrupt: true}
	}

	// Take the newest n from the durable dataset, then reverse to NEWEST-FIRST.
	start := 0
	if len(recs) > n {
		start = len(recs) - n
	}
	sel := recs[start:]
	out := make([]protection.AlertTransition, 0, len(sel))
	for i := len(sel) - 1; i >= 0; i-- {
		out = append(out, sel[i].rec)
	}
	return protection.TransitionReadResult{
		Transitions:                out,
		FileDropped:                fd,
		RetentionMetaInconsistent: metaInc,
	}
}

// ReadBefore implements protection.AlertTransitionStore (Phase 32): a bounded
// durable PAGE — up to n persisted transitions preceding `cursor`, NEWEST-FIRST.
// An empty cursor starts from the newest record.
//
// P32-I11 (Durable order authority): the page boundary is an INDEX INTO THE
// PARSED DURABLE ORDER. The cursor only identifies a record; it never orders
// anything, and timestamps are not consulted.
//
// P32-I12 (Cursor survives rewrite): because the identity is the durable
// sequence, an eviction rewrite — including one that drops older duplicates —
// never invalidates a cursor whose record is still retained.
func (s *FileBackedTransitionStore) ReadBefore(ctx context.Context, cursor string, n int) protection.TransitionPageResult {
	if err := ctx.Err(); err != nil {
		return protection.TransitionPageResult{LoadErr: err}
	}
	if n <= 0 {
		return protection.TransitionPageResult{}
	}
	if n > protection.DurableReadMaxLimit {
		n = protection.DurableReadMaxLimit
	}
	if int64(n) > protection.FileTransitionCapacity {
		n = int(protection.FileTransitionCapacity)
	}

	// An empty cursor means "start from the newest": no token to validate.
	var seq int64
	if cursor != "" {
		parsed, err := parseCursor(cursor)
		if err != nil {
			return protection.TransitionPageResult{LoadErr: err}
		}
		seq = parsed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openErr != nil {
		return protection.TransitionPageResult{LoadErr: s.openErr}
	}
	st, err := os.Stat(s.path)
	if err != nil {
		return protection.TransitionPageResult{
			LoadErr: fmt.Errorf("durable read stat: %w", err),
		}
	}
	if st.Size() > durableReadMaxBytes {
		return protection.TransitionPageResult{
			LoadErr: fmt.Errorf("%w: %d bytes > %d", ErrDurableBudgetExceeded, st.Size(), durableReadMaxBytes),
		}
	}
	if err := ctx.Err(); err != nil {
		return protection.TransitionPageResult{LoadErr: err}
	}

	recs, fd, _, metaInc, corrupt, _, err := s.scanRecords()
	if err != nil {
		return protection.TransitionPageResult{LoadErr: err}
	}
	if corrupt {
		// Corrupt durable history is never served as authoritative data.
		return protection.TransitionPageResult{Corrupt: true}
	}

	// Exclusive upper bound in durable order (records with index < end).
	end := len(recs)
	if cursor != "" {
		p, err := resolveCursor(recs, seq)
		if err != nil {
			return protection.TransitionPageResult{LoadErr: err}
		}
		end = p
	}
	start := 0
	if end > n {
		start = end - n
	}
	sel := recs[start:end]
	out := make([]protection.AlertTransition, 0, len(sel))
	for i := len(sel) - 1; i >= 0; i-- {
		out = append(out, sel[i].rec) // NEWEST-FIRST (P32-I5)
	}
	hasMore := start > 0
	next := ""
	if hasMore {
		// Mint from the OLDEST record of this page, so the next request
		// continues strictly before it. Never let a client derive this.
		next = mintCursor(recs[start].seq)
	}
	return protection.TransitionPageResult{
		Transitions:                out,
		FileDropped:                fd,
		RetentionMetaInconsistent: metaInc,
		HasMore:                   hasMore,
		NextCursor:                next,
	}
}

// FailedTransitionStore is a DEGRADED protection.AlertTransitionStore used when
// the durable transition file could not be opened at all (P30-I11-impl).
//
// It exists so the wiring can hand the tracker a NON-NIL store that reports the
// failure: the tracker then exposes load_error=true / available=false over the
// read API. Without it, the wiring would fall back to a clean in-memory
// tracker and an unreadable persisted history would become indistinguishable
// from "there is no history".
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

// ReadBefore reports the same failure (P32-I4/P31-I9): a degraded store has no
// durable pages to serve, and must never look like "an empty page".
func (s *FailedTransitionStore) ReadBefore(ctx context.Context, cursor string, n int) protection.TransitionPageResult {
	return protection.TransitionPageResult{LoadErr: s.err}
}

// Close implements protection.AlertTransitionStore.
func (s *FailedTransitionStore) Close() error { return nil }
