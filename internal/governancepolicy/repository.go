package governancepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/governance"
)

// Repository is the persistence contract for policies (ADR-030 B6: the
// repository is the SINGLE owner of policy persistence; governance.Engine owns
// NONE of this). Implementations may back it with file / sqlite / etc., but ALL
// must satisfy the same semantic contract defined here.
//
// Every method is a pure persistence operation — it never evaluates, never
// executes, never forms an execution bridge (B9). Persistence verbs
// (Save/Get/List/Archive/Activate/Deactivate/NextRevision) are intentionally
// LEGAL; only execution verbs are forbidden (see guards_test.go).
type Repository interface {
	// Save upserts the latest revision of a policy (creating it, or bumping the
	// revision of an existing PolicyID).
	Save(p PolicyRecord) (PolicyRecord, error)
	// Get returns the latest revision of a policy (ok=false when absent).
	Get(policyID string) (PolicyRecord, bool, error)
	// List returns all policies in stable PolicyID order.
	List() ([]PolicyRecord, error)
	// Archive marks a policy archived (retained for audit, no longer evaluated).
	Archive(policyID string) error
	// Activate marks a policy active (in force).
	Activate(policyID string) error
	// Deactivate marks a policy inactive (draft-equivalent, not evaluated).
	Deactivate(policyID string) error
	// NextRevision returns the next unused revision number for a PolicyID
	// (1 when the policy is new).
	NextRevision(policyID string) (int, error)

	// --- Phase 17 Management mutation contract (ADR-036, signed R77-A) -------
	//
	// These two — and ONLY these two — are the revision-aware mutation
	// primitives. Save/Activate/Deactivate/Archive above are historical
	// compatibility interfaces with NO revision semantics; the Management
	// surface is forbidden to call them (ADR-036 §3.6, enforced by test).

	// CompareAndSave atomically replaces the stored record for rec.PolicyID iff
	// the currently stored revision equals expectedRevision. expectedRevision
	// == 0 means "must not already exist". Returns ErrRevisionConflict on
	// mismatch. The comparison, the mutation and the revision increment happen
	// inside the repository, in one protected read-modify-write (CAS-1…CAS-6).
	CompareAndSave(rec PolicyRecord, expectedRevision int) (PolicyRecord, error)

	// CompareAndTransition atomically moves policyID to targetStatus iff the
	// currently stored revision equals expectedRevision and the transition is
	// legal. Returns ErrRevisionConflict on revision mismatch (checked FIRST,
	// CT-9), ErrIllegalTransition otherwise. A self-transition succeeds as a
	// no-op without bumping the revision (CT-8).
	CompareAndTransition(policyID string, expectedRevision int, targetStatus PolicyStatus) (PolicyRecord, error)
}

// fileRepository stores one JSON file per policy under dir, named by a
// filesystem-safe encoding of PolicyID. The file holds the single latest
// PolicyRecord; the repository owns the revision bookkeeping, and Save replaces
// the file with the newly versioned record.
//
// Store hardening (ADR-036 §3.2.1, architecture constraint — NOT an
// optimisation). Without all five, CompareAndSave is a CAS in name only:
//  1. a mutex covering EVERY read-modify-write path (Save, setState,
//     CompareAndSave, CompareAndTransition);
//  2. writes land in a temporary file first;
//  3. that file is fsync'd before publication;
//  4. publication is an atomic rename;
//  5. the revision comparison stays in here — never in a handler (CAS-6).
//
// The lock is held by the exported methods only. Unexported helpers (read,
// write, setStateLocked) assume it is already held and must never re-acquire
// it — that would self-deadlock on a non-reentrant mutex.
type fileRepository struct {
	mu  sync.Mutex
	dir string
}

// NewFileRepository constructs a file-backed repository, creating dir if needed.
// An empty dir is rejected (ErrInvalidID) to avoid implicit / ambiguous state
// (B6 — the store must be an explicit, single owner).
func NewFileRepository(dir string) (Repository, error) {
	if dir == "" {
		return nil, ErrInvalidID
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &fileRepository{dir: dir}, nil
}

// safeName validates and returns a filesystem-safe fragment for a PolicyID,
// rejecting anything that could escape the store directory.
func safeName(policyID string) (string, error) {
	if policyID == "" {
		return "", ErrInvalidID
	}
	// Reject separators and traversal attempts: a clean, single-name component
	// only. filepath.Clean would strip "./", so require equality.
	if policyID != filepath.Base(policyID) || strings.ContainsAny(policyID, "/\\") {
		return "", ErrInvalidID
	}
	return policyID, nil
}

func (r *fileRepository) pathFor(policyID string) (string, error) {
	name, err := safeName(policyID)
	if err != nil {
		return "", err
	}
	return filepath.Join(r.dir, "policy-"+name+".json"), nil
}

func (r *fileRepository) read(policyID string) (PolicyRecord, bool, error) {
	p, err := r.pathFor(policyID)
	if err != nil {
		return PolicyRecord{}, false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return PolicyRecord{}, false, nil
		}
		return PolicyRecord{}, false, err
	}
	var rec PolicyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return PolicyRecord{}, false, err
	}
	return rec, true, nil
}

// write publishes rec atomically: marshal -> temp file in the SAME directory ->
// fsync -> close -> rename over the target (ADR-036 §3.2.1 hardening 2-4).
//
// Same directory matters: rename is only atomic within a filesystem. fsync
// before rename matters: without it a crash can publish a zero-length or
// truncated file that unmarshals into an empty PolicyRecord — i.e. silent data
// loss that CAS cannot detect, because the revision it would compare against is
// gone. The previous implementation used os.WriteFile, which is a truncating
// in-place write and had exactly that failure mode.
//
// SCOPE LIMIT (verified empirically during 17.2): the guarantee is
// PROCESS-LOCAL. On Windows, renaming over a target that another handle
// currently has open fails with "Access is denied" instead of replacing it, so
// publication is only reliable while r.mu serialises writers — and a mutex
// spans one process. Two processes sharing a store directory would see rename
// failures, not last-writer-wins. Acceptable because the composition root
// builds one repository per process, but this store must never be presented as
// a multi-process CAS.
//
// Caller MUST hold r.mu.
func (r *fileRepository) write(rec PolicyRecord) error {
	p, err := r.pathFor(rec.PolicyID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.dir, ".policy-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// From here on every failure path removes the temp file: a crashed write
	// must never leave a partial file that List() could pick up. It cannot —
	// List filters on the "policy-*.json" prefix — but leaking temp files into
	// the store directory is still a defect.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (r *fileRepository) Save(rec PolicyRecord) (PolicyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saveLocked(rec)
}

// saveLocked is the original Save body. Behaviour is unchanged (Phase 14 must
// not shift under Phase 17); only the surrounding lock is new.
//
// Caller MUST hold r.mu.
func (r *fileRepository) saveLocked(rec PolicyRecord) (PolicyRecord, error) {
	if rec.PolicyID == "" {
		return PolicyRecord{}, ErrInvalidID
	}
	existing, ok, err := r.read(rec.PolicyID)
	if err != nil {
		return PolicyRecord{}, err
	}
	// Bump revision on save of an existing PolicyID (B8 — Revision is a version
	// attribute). A caller-supplied non-zero revision is honored as-is (used by
	// Create which already computed the next revision).
	if ok && rec.Revision == 0 {
		rec.Revision = existing.Revision + 1
	}
	if rec.CreatedAt.IsZero() {
		if ok {
			rec.CreatedAt = existing.CreatedAt
		} else {
			rec.CreatedAt = time.Now()
		}
	}
	rec.UpdatedAt = time.Now()
	if rec.Status == "" {
		rec.Status = StatusDraft
	}
	if err := r.write(rec); err != nil {
		return PolicyRecord{}, err
	}
	return rec, nil
}

func (r *fileRepository) Get(policyID string) (PolicyRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read(policyID)
}

func (r *fileRepository) List() ([]PolicyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	out := make([]PolicyRecord, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "policy-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "policy-"), ".json")
		rec, ok, err := r.read(id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PolicyID < out[j].PolicyID })
	return out, nil
}

// setState transitions a policy's Status, persisting the change. It never
// evaluates and never executes (B7/B9).
//
// LEGACY — historical compatibility only. It does NOT bump Revision and applies
// NO legality check, so it is not, and must never be presented as, a
// revision-aware API (ADR-036 §4). Phase 17 mutations go through
// CompareAndSave / CompareAndTransition.
func (r *fileRepository) setState(policyID string, st PolicyStatus, activated bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.setStateLocked(policyID, st, activated)
}

// Caller MUST hold r.mu.
func (r *fileRepository) setStateLocked(policyID string, st PolicyStatus, activated bool) error {
	rec, ok, err := r.read(policyID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	rec.Status = st
	if activated {
		rec.ActivatedAt = time.Now()
	}
	rec.UpdatedAt = time.Now()
	return r.write(rec)
}

func (r *fileRepository) Archive(policyID string) error {
	return r.setState(policyID, StatusArchived, false)
}

func (r *fileRepository) Activate(policyID string) error {
	return r.setState(policyID, StatusActive, true)
}

func (r *fileRepository) Deactivate(policyID string) error {
	return r.setState(policyID, StatusDraft, false)
}

func (r *fileRepository) NextRevision(policyID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok, err := r.read(policyID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 1, nil
	}
	return rec.Revision + 1, nil
}

// ---------------------------------------------------------------------------
// Phase 17 Management mutation contract (ADR-036 §3.2.1 / §3.2.2, signed R77-A)
// ---------------------------------------------------------------------------

// CompareAndSave implements CAS-1…CAS-6. The comparison, the mutation and the
// revision increment happen here, under one lock, in one atomic publication —
// never split across a handler (CAS-6 / MUST-P17-12).
func (r *fileRepository) CompareAndSave(rec PolicyRecord, expectedRevision int) (PolicyRecord, error) {
	if rec.PolicyID == "" {
		return PolicyRecord{}, ErrInvalidID
	}
	if expectedRevision < 0 {
		return PolicyRecord{}, ErrInvalidID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok, err := r.read(rec.PolicyID)
	if err != nil {
		return PolicyRecord{}, err
	}

	// CAS-1: expectedRevision == 0 means "must not already exist".
	if expectedRevision == 0 {
		if ok {
			return PolicyRecord{}, ErrRevisionConflict
		}
		rec.Revision = 1
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = time.Now()
		}
		// A create always lands as a Draft: Status is repository-owned, so a
		// caller cannot conjure an Active policy in one step (P17-4 — no
		// lifecycle decision at the write boundary).
		rec.Status = StatusDraft
		rec.ActivatedAt = time.Time{}
		rec.UpdatedAt = time.Now()
		if err := r.write(rec); err != nil {
			return PolicyRecord{}, err
		}
		return rec, nil
	}

	// CAS-2: expectedRevision > 0 must match the stored revision exactly.
	if !ok {
		return PolicyRecord{}, ErrNotFound
	}
	if existing.Revision != expectedRevision {
		return PolicyRecord{}, ErrRevisionConflict
	}

	// Update is Draft-only (ADR-036 §3.4). Changing an Active policy means
	// authoring the next Draft and activating it — the Phase 14 PolicyID +
	// Revision pairing is preserved either way.
	if existing.Status != StatusDraft {
		return PolicyRecord{}, ErrIllegalTransition
	}

	// CAS-4: mutation and revision increment are persisted in the SAME write.
	// Repository-owned fields are carried over from the stored record rather
	// than taken from the caller: the caller supplies content, never lifecycle.
	rec.Revision = existing.Revision + 1
	rec.Status = existing.Status
	rec.CreatedAt = existing.CreatedAt
	rec.ActivatedAt = existing.ActivatedAt
	rec.UpdatedAt = time.Now()
	if err := r.write(rec); err != nil {
		return PolicyRecord{}, err
	}
	return rec, nil
}

// admittedTransitions is the lifecycle state machine CREATED by Phase 17
// (ADR-036 §3.2.2 fact 2: no legality check existed anywhere before). Keyed
// from -> set of admitted targets. Self-transitions are deliberately absent:
// they are handled earlier as CT-8 no-ops, not as machine edges.
var admittedTransitions = map[PolicyStatus]map[PolicyStatus]bool{
	StatusDraft:  {StatusActive: true, StatusArchived: true},
	StatusActive: {StatusDraft: true, StatusArchived: true},
	// StatusArchived is terminal — no outgoing edges.
}

// CompareAndTransition implements CT-1…CT-9. Order is fixed and load-bearing:
//
//	load -> compare revision -> (mismatch: ErrRevisionConflict)
//	     -> self-transition? -> (yes: no-op success, NO revision bump)
//	     -> validate transition -> (illegal: ErrIllegalTransition)
//	     -> mutate + bump revision, one atomic publication
//
// CT-9 puts the revision check first on purpose: a caller holding a stale view
// must refetch before its intent can even be judged, otherwise it would be sent
// to fix the wrong problem.
func (r *fileRepository) CompareAndTransition(policyID string, expectedRevision int, targetStatus PolicyStatus) (PolicyRecord, error) {
	if policyID == "" {
		return PolicyRecord{}, ErrInvalidID
	}
	switch targetStatus {
	case StatusDraft, StatusActive, StatusArchived:
	default:
		return PolicyRecord{}, ErrIllegalTransition
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok, err := r.read(policyID)
	if err != nil {
		return PolicyRecord{}, err
	}
	if !ok {
		return PolicyRecord{}, ErrNotFound
	}

	// CT-1 / CT-2 / CT-9: revision first, always.
	if rec.Revision != expectedRevision {
		return PolicyRecord{}, ErrRevisionConflict
	}

	// CT-8: already in the target state — succeed, return unchanged, do NOT
	// bump. Bumping here would make every retry mint a new revision and
	// invalidate the caller's If-Match token, which is the opposite of
	// idempotent.
	if rec.Status == targetStatus {
		return rec, nil
	}

	if !admittedTransitions[rec.Status][targetStatus] {
		return PolicyRecord{}, ErrIllegalTransition
	}

	// CT-3: status check + revision check + mutation + revision bump are one
	// protected read-modify-write. The bump is load-bearing, not bookkeeping:
	// without it two concurrent transitions holding the same expectedRevision
	// would both pass the compare and both commit.
	rec.Status = targetStatus
	rec.Revision++
	if targetStatus == StatusActive {
		rec.ActivatedAt = time.Now()
	}
	rec.UpdatedAt = time.Now()
	if err := r.write(rec); err != nil {
		return PolicyRecord{}, err
	}
	return rec, nil
}

// SameContent reports whether two records carry the same caller-authored
// content, ignoring every repository-owned field (Revision, Status, timestamps).
//
// It exists for the create-replay branch of the idempotency contract
// (ADR-036 §3.4): when CompareAndSave(rec, 0) reports ErrRevisionConflict the
// Management surface performs a READ-ONLY Get and compares payloads to choose
// 200 (identical replay) or 409 (same ID, different payload). That branch never
// writes, so it is not the forbidden Get->check->Save.
//
// Comparison is PAYLOAD equality, not semantic equality: rule ORDER counts,
// even though governance.Evaluate sorts by (Priority, RuleID) and would treat
// two orderings alike. This is deliberate and fail-closed. A re-sent HTTP
// request carries its rules in the same order, so a reorder means the operator
// sent something else; answering 409 costs them one refetch, whereas answering
// 200 would claim we stored what they sent when we stored a different array.
func SameContent(a, b PolicyRecord) bool {
	if a.PolicyID != b.PolicyID {
		return false
	}
	return reflect.DeepEqual(normalizeRules(a.Rules), normalizeRules(b.Rules))
}

// normalizeRules returns a nil-vs-empty-insensitive copy so that a replay
// submitting `[]Rule{}` matches a stored `nil` — they mean the same thing and
// must not be reported as a payload difference.
func normalizeRules(rs []governance.Rule) []governance.Rule {
	if len(rs) == 0 {
		return nil
	}
	return rs
}
