package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
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
	recs, fd, mi, corrupt, err := s.scanRecords()
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

// scanRecords is the Phase 32 generalisation of scan(): it returns each parsed
// transition TOGETHER with the byte offset of its line, which is what lets the
// store mint opaque cursors at zero extra I/O cost. Corruption semantics are
// byte-for-byte those of P30-I12 (same trailing/middle discrimination), so the
// startup Load and the paged durable read can never disagree.
func (s *FileBackedTransitionStore) scanRecords() (records []scannedRecord, fileDropped int64, metaInconsistent bool, corrupt bool, hardErr error) {
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

	// Collect the non-empty transition lines together with their byte offsets,
	// so "trailing" is judged against the LAST NON-EMPTY line (a trailing blank
	// must not mask it) and so cursors can be minted without extra I/O.
	type lineAt struct {
		text   string
		offset int64
	}
	txLines := make([]lineAt, 0, len(lines))
	off := int64(0)
	for idx, ln := range lines {
		if idx < start || strings.TrimSpace(ln) == "" {
			off += int64(len(ln)) + 1 // account for the consumed/skipped line
			continue
		}
		txLines = append(txLines, lineAt{text: ln, offset: off})
		off += int64(len(ln)) + 1
	}

	records = make([]scannedRecord, 0, len(txLines))
	last := len(txLines) - 1
	for i, la := range txLines {
		var t protection.AlertTransition
		if err := json.Unmarshal([]byte(la.text), &t); err != nil || t.At.IsZero() {
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
		records = append(records, scannedRecord{rec: t, offset: la.offset})
	}
	s.count = int64(len(records))
	s.fileDropped = fileDropped
	return records, fileDropped, metaInconsistent, false, nil
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

// ---------------------------------------------------------------------------
// Phase 32 — opaque durable cursor
// ---------------------------------------------------------------------------
//
// A cursor is an OPAQUE token:
//
//	base64url("v1:" + offset + ":" + fingerprint + ":" + ordinal)
//
//   - offset      is ONLY a lookup hint. It is NOT an order key and NOT an
//                 identity: an eviction rewrite moves every offset.
//   - fingerprint is the record CONTENT IDENTITY (FNV-1a 64 of canonical
//                 fields). It is NOT an order key either.
//   - ordinal     disambiguates records whose content is byte-identical: it is
//                 the count of same-fingerprint records preceding this one in
//                 durable order. Transitions with identical canonical fields are
//                 LEGAL and may repeat, so content identity alone would collapse
//                 them into one "ambiguous" match and wrongly expire a cursor
//                 whose record is still retained (R146=B). The discriminator
//                 lives only inside the cursor — P32-I8 forbids touching the
//                 JSONL record shape.
//
// P32-I11 (Durable order authority): paging order is the order in which records
// actually appear in the durable file. Offset arithmetic and timestamp
// comparison must never decide "what comes before the cursor".
//
// P32-I12 (Cursor survives rewrite): a rewrite changes offsets but does NOT
// invalidate a cursor whose record is still retained. Expiry is decided solely
// by "is the record still present", never by "did the offset move".

const cursorVersion = "v1"

// recordFingerprint is the durable IDENTITY of a transition record, computed
// from canonical fields only. It deliberately does NOT depend on the record's
// position in the file, so it still identifies the record after a rewrite.
func recordFingerprint(t protection.AlertTransition) uint64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, t.At.UTC().Format(time.RFC3339Nano))
	_, _ = io.WriteString(h, "|")
	_, _ = io.WriteString(h, strconv.FormatBool(t.From))
	_, _ = io.WriteString(h, "|")
	_, _ = io.WriteString(h, strconv.FormatBool(t.To))
	_, _ = io.WriteString(h, "|")
	_, _ = io.WriteString(h, strconv.FormatInt(t.UnknownRate, 10))
	_, _ = io.WriteString(h, "|")
	_, _ = io.WriteString(h, strconv.FormatInt(t.Threshold, 10))
	return h.Sum64()
}

// occurrenceOrdinal counts how many records BEFORE index i share i's
// fingerprint. Transitions whose canonical fields are byte-identical are LEGAL
// and may legitimately repeat (e.g. two edges landing on the same timestamp
// with the same rates), so content identity alone cannot distinguish them.
//
// P32-I8 forbids adding a field to the JSONL record, so this discriminator
// lives ONLY inside the opaque cursor (R146=B).
func occurrenceOrdinal(records []scannedRecord, i int) int {
	fp := recordFingerprint(records[i].rec)
	n := 0
	for j := 0; j < i; j++ {
		if recordFingerprint(records[j].rec) == fp {
			n++
		}
	}
	return n
}

// mintCursor builds the opaque token for records[i]:
// base64url("v1:" + offset + ":" + fingerprint + ":" + ordinal)
func mintCursor(records []scannedRecord, i int) string {
	raw := cursorVersion + ":" +
		strconv.FormatInt(records[i].offset, 10) + ":" +
		strconv.FormatUint(recordFingerprint(records[i].rec), 16) + ":" +
		strconv.Itoa(occurrenceOrdinal(records, i))
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// parsedCursor is the decoded form of an opaque cursor.
type parsedCursor struct {
	offset      int64
	fingerprint uint64
	ordinal     int
}

func parseCursor(cur string) (parsedCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return parsedCursor{}, fmt.Errorf("%w: undecodable", protection.ErrInvalidCursor)
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 4 || parts[0] != cursorVersion {
		return parsedCursor{}, fmt.Errorf("%w: bad shape/version", protection.ErrInvalidCursor)
	}
	off, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || off < 0 {
		return parsedCursor{}, fmt.Errorf("%w: bad offset", protection.ErrInvalidCursor)
	}
	fp, err := strconv.ParseUint(parts[2], 16, 64)
	if err != nil {
		return parsedCursor{}, fmt.Errorf("%w: bad fingerprint", protection.ErrInvalidCursor)
	}
	ord, err := strconv.Atoi(parts[3])
	if err != nil || ord < 0 {
		return parsedCursor{}, fmt.Errorf("%w: bad ordinal", protection.ErrInvalidCursor)
	}
	return parsedCursor{offset: off, fingerprint: fp, ordinal: ord}, nil
}

// scannedRecord pairs a parsed transition with the byte offset of its line in
// the durable file. Offsets are recorded during scan, so cursors cost no I/O.
type scannedRecord struct {
	rec    protection.AlertTransition
	offset int64
}

// resolveCursor locates the cursor's record index within the parsed durable
// order:
//
//	P32-I11: the index comes from the durable order; offset is only a lookup
//	         hint and fingerprint only a content identity — neither orders
//	         anything, and timestamps never participate.
//	P32-I12: an offset change never expires a cursor; only a missing record
//	         does. Resolution therefore falls back to (fingerprint, ordinal).
//
// The ordinal is what keeps legal duplicate records apart (R146=B): records
// with byte-identical canonical fields are distinct occurrences, so they are
// selected by "the Nth record with this content", not collapsed into an
// ambiguous match.
func resolveCursor(records []scannedRecord, c parsedCursor) (int, error) {
	// Fast path: exact offset hit with a matching identity. This is
	// unambiguous when the file layout has not changed since minting.
	for i := range records {
		if records[i].offset == c.offset && recordFingerprint(records[i].rec) == c.fingerprint {
			return i, nil
		}
	}
	// Slow path: a rewrite moved offsets. Locate every record sharing the
	// content identity, then select the occurrence the cursor was minted for.
	matches := make([]int, 0, 4)
	for i := range records {
		if recordFingerprint(records[i].rec) == c.fingerprint {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		// Gone from retention. Never restart from the newest page (that would
		// silently duplicate/omit records) — report it, P32-I10.
		return -1, fmt.Errorf("%w: record no longer retained", protection.ErrCursorExpired)
	}
	if c.ordinal >= len(matches) {
		// The content still exists, but that specific occurrence was evicted.
		// Never guess which surviving duplicate to use, P32-I10.
		return -1, fmt.Errorf("%w: occurrence %d no longer retained (%d remain)",
			protection.ErrCursorExpired, c.ordinal, len(matches))
	}
	return matches[c.ordinal], nil
}

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

// ReadBefore implements protection.AlertTransitionStore (Phase 32): a bounded
// durable PAGE — up to n persisted transitions preceding `cursor`, NEWEST-FIRST.
// An empty cursor starts from the newest record.
//
// P32-I11 (Durable order authority): the page boundary is an INDEX INTO THE
// PARSED DURABLE ORDER. The cursor's offset is only a lookup hint and its
// fingerprint only an identity check; neither participates in ordering, and
// timestamps never do.
//
// P32-I12 (Cursor survives rewrite): an eviction rewrite moves offsets but does
// not invalidate a cursor whose record is still retained — resolution falls back
// to identity matching. Only a genuinely missing record expires a cursor.
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
	var pc parsedCursor
	if cursor != "" {
		parsed, err := parseCursor(cursor)
		if err != nil {
			return protection.TransitionPageResult{LoadErr: err}
		}
		pc = parsed
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

	recs, fd, metaInc, corrupt, err := s.scanRecords()
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
		p, err := resolveCursor(recs, pc)
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
		next = mintCursor(recs, start)
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

// ReadBefore reports the same failure (P32-I4/P31-I9): a degraded store has no
// durable pages to serve, and must never look like "an empty page".
func (s *FailedTransitionStore) ReadBefore(ctx context.Context, cursor string, n int) protection.TransitionPageResult {
	return protection.TransitionPageResult{LoadErr: s.err}
}

// Close implements protection.AlertTransitionStore.
func (s *FailedTransitionStore) Close() error { return nil }
