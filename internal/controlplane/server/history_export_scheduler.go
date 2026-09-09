package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// historyExportFileBase is the on-disk snapshot prefix (Phase 34). One Tick
// computes ONE shared snapshot base identity alert-transitions-<ts>[.N]; every
// format appends its own extension, so a snapshot's artifacts share the same
// collision ordinal: alert-transitions-<ts>[.N].<fmt> (R162/B — base-level
// collision retry, never per-format retry suffixes).
const historyExportFileBase = "alert-transitions"

// HistoryExportConfig configures the Phase 34 scheduled periodic export of the
// durable alert-transition history to local files. The scheduler is OPT-IN: the
// composition root only constructs it when BOTH a positive interval and a
// destination dir are supplied; otherwise it stays nil and nothing is written.
type HistoryExportConfig struct {
	// Store is the durable alert-transition store. REQUIRED: a nil Store is a
	// fail-fast config error at New time (R160 — durable-only export never
	// silently degrades to a no-op scheduler). A configured store that later
	// returns a ReadAll error/corrupt still makes the tick SKIP at runtime
	// (degraded store), but nil is never accepted.
	Store protection.AlertTransitionStore
	// Dir is the destination directory for snapshot files. Required when enabled.
	Dir string
	// Interval is the tick period. Required > 0 when enabled.
	Interval time.Duration
	// Formats lists the export encodings to materialize each tick. All formats
	// share ONE snapshot base identity per tick; a per-format write failure is
	// reported explicitly (P34-I2 — no faked success). Only "json" and "csv"
	// are accepted.
	Formats []string
	// Retain is the local snapshot retention cap. 0 keeps ALL snapshots; >0
	// keeps at most Retain newest SNAPSHOTS. One scheduled export (one
	// ExportedAt) is ONE snapshot regardless of how many formats (.json/.csv)
	// it materializes; a snapshot and all its artifact files are removed together
	// (P34-I6 bounded local retention, corrected to snapshot units per R161/B).
	// Default 96.
	Retain int
	// Logger receives lifecycle/error diagnostics. May be nil.
	Logger *slog.Logger
	// Clock is the scheduler-side wall clock (LastRunAt / prune / tests only).
	// ExportedAt is the STORE's provenance and is NEVER derived from Clock
	// (P34-CLOCK-1). Defaults to time.Now.
	Clock func() time.Time
}

// HistoryExportStatus is the read-only scheduler state surfaced via
// GET /management/v1/protection/alerts/history/export/scheduler.
type HistoryExportStatus struct {
	Enabled         bool      `json:"enabled"`
	Running         bool      `json:"running"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`       // scheduler attempt time (scheduler clock)
	LastExportedAt  *time.Time `json:"last_exported_at,omitempty"`  // store provenance (res.ExportedAt)
	LastError       string    `json:"last_error,omitempty"`
	PruneError      string    `json:"prune_error,omitempty"`
	SkipCount       int64     `json:"skip_count"`
	Published       int64     `json:"published"`
	Failed          int64     `json:"failed"`
	Dir             string    `json:"dir,omitempty"`
	Interval        string    `json:"interval,omitempty"`
	Formats         []string  `json:"formats,omitempty"`
	Retain          int       `json:"retain"`
}

// HistoryExportScheduler materializes the durable alert-transition history to
// local files on a fixed interval (Phase 34). It reuses the store's ReadAll and
// the Phase 33 pure serializers so the on-disk snapshots are byte-identical in
// shape to the on-demand HTTP export. Publication is atomic and no-replace:
// a crash mid-write never leaves a half-written formal file, and an existing
// snapshot is never overwritten (P34-I3).
type HistoryExportScheduler struct {
	cfg    HistoryExportConfig
	clock  func() time.Time
	logger *slog.Logger

	mu       sync.Mutex
	started  bool
	running  bool // a tick is currently active (non-reentrant guard, P34-I1)
	lastRunAt      time.Time
	lastExportedAt time.Time
	lastError      string
	pruneError     string
	skipCount      int64
	published      int64
	failed         int64

	done chan struct{}
}

// NewHistoryExportScheduler validates the config and builds a scheduler
// (P34-I5 fail-fast on illegal config). It does NOT start ticking; call
// Start(parent) to begin.
func NewHistoryExportScheduler(cfg HistoryExportConfig) (*HistoryExportScheduler, error) {
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("history export: interval must be > 0")
	}
	if cfg.Dir == "" {
		return nil, fmt.Errorf("history export: dir must be set")
	}
	if len(cfg.Formats) == 0 {
		return nil, fmt.Errorf("history export: at least one format required")
	}
	for _, f := range cfg.Formats {
		switch f {
		case "json", "csv":
		default:
			return nil, fmt.Errorf("history export: unsupported format %q (use json|csv)", f)
		}
	}
	if cfg.Retain < 0 {
		return nil, fmt.Errorf("history export: retain must be >= 0 (0 = keep all)")
	}
	// R160 (durable-only): a nil Store is a fail-fast config error, never a
	// silent no-op scheduler. A non-nil but erroring store is handled at Tick
	// time (degraded-store skip), not here.
	if cfg.Store == nil {
		return nil, fmt.Errorf("history export: store is required (durable-only export)")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &HistoryExportScheduler{
		cfg:    cfg,
		clock:  clock,
		logger: cfg.Logger,
	}, nil
}

// Start launches the ticker loop bound to parent's lifecycle. It is
// fire-and-forget (returns immediately) and idempotent: a second Start on the
// same instance is a no-op (P34-I4-ARCH-1). Shutdown completion is observable
// via Done().
func (s *HistoryExportScheduler) Start(parent context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.done = make(chan struct{})
	s.mu.Unlock()

	go s.run(parent)
}

func (s *HistoryExportScheduler) run(parent context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
			s.Tick(parent)
		}
	}
}

// Tick performs one export attempt synchronously. It is the unit of work the
// ticker calls; tests also call it directly for deterministic assertions. A
// Tick already in flight rejects a concurrent Tick (skip, not parallel) — the
// single-active invariant (P34-I1).
func (s *HistoryExportScheduler) Tick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.skipCount++
		s.mu.Unlock()
		return
	}
	s.running = true
	s.lastRunAt = s.clock()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	// Store is guaranteed non-nil by New (R160 fail-fast). A configured store
	// that errors/corrupts at runtime is a degraded-store SKIP further below.
	res := s.cfg.Store.ReadAll(ctx)
	if res.LoadErr != nil {
		s.mu.Lock()
		s.lastError = "read error: " + res.LoadErr.Error()
		// Published/Failed are LAST-TICK format counts (R160/R162): a skipped
		// tick attempted no formats, so both reset to zero.
		s.published, s.failed = 0, 0
		s.mu.Unlock()
		return
	}
	if res.Corrupt {
		s.mu.Lock()
		s.lastError = "durable history corrupt — skipped"
		s.published, s.failed = 0, 0
		s.mu.Unlock()
		return
	}

	// Publish ALL formats of this tick under ONE shared snapshot base identity
	// (R162/B). Collision retries advance one ordinal for the whole group;
	// per-format write failures stay independent and are reported explicitly,
	// never masked as a full success (P34-I2).
	pubErrs := s.publishSnapshot(res)

	s.mu.Lock()
	switch {
	case len(pubErrs) == 0:
		// Full success: record provenance, clear any prior error.
		s.lastExportedAt = res.ExportedAt
		s.lastError = ""
	case len(pubErrs) < len(s.cfg.Formats):
		// Partial success: provenance recorded; explicit partial error.
		s.lastExportedAt = res.ExportedAt
		s.lastError = "partial export: " + strings.Join(pubErrs, "; ")
	default:
		// Total failure: no file was published.
		s.lastError = "export failed: " + strings.Join(pubErrs, "; ")
	}
	// Published/Failed are the LAST tick's per-format counts (R160), NOT
	// cumulative counters (R162/B fix).
	s.published = int64(len(s.cfg.Formats) - len(pubErrs))
	s.failed = int64(len(pubErrs))
	s.mu.Unlock()

	// Prune is independent of publish success (P34-STATE-1): its error is
	// reported separately and never overwrites lastError/lastExportedAt.
	if err := s.prune(); err != nil {
		s.mu.Lock()
		s.pruneError = err.Error()
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		s.pruneError = ""
		s.mu.Unlock()
	}
}

// publishSnapshot atomically materializes ALL formats of one tick under a
// single shared snapshot base identity (P34-I3, R160/R162). Scheme per ordinal:
//  1. reserve every format's slot via sentinel files (O_CREATE|O_EXCL) — one
//     shared base = alert-transitions-<ts>[.<ordinal>], each format appending
//     only its extension. Any EEXIST during reservation means THIS base ordinal
//     is occupied, so the WHOLE group advances (no-replace preserved for every
//     format; never per-format retry suffixes — R162/B);
//  2. write each format's .tmp (Sync before link). Write failures are
//     PER-FORMAT errors and do NOT move the ordinal (P34-I2 partial export);
//  3. pre-check every target with Lstat BEFORE linking: if a legacy/external file
//     already occupies ANY format's target the group advances without linking at
//     all, so the common collision case never deletes anything. os.Link is still
//     no-replace, so a residual race-EEXIST rolls the group forward — and that
//     rollback is OWNERSHIP-SAFE (removeOwnArtifact proves the path is still the
//     file we published; a foreign replacement is reported, never deleted).
// A crash between steps 2 and 3 leaves only .tmp + sentinel debris; never a
// corrupt formal file, and never a silently-overwritten one.
// Returns one error string per failed format (empty = full success).
func (s *HistoryExportScheduler) publishSnapshot(res protection.TransitionReadResult) []string {
	// Filesystem-safe timestamp: RFC3339Nano has ':' which is Windows-invalid,
	// so use a colon-free layout. res.ExportedAt is store provenance (P34-CLOCK-1).
	safe := res.ExportedAt.UTC().Format("20060102T150405.999999999") + "Z"
	base := fmt.Sprintf("%s-%s", historyExportFileBase, safe)

	for ordinal := 0; ordinal < 100; ordinal++ {
		names := make(map[string]string, len(s.cfg.Formats))
		for _, f := range s.cfg.Formats {
			name := base
			if ordinal > 0 {
				name = fmt.Sprintf("%s.%d", base, ordinal)
			}
			names[f] = name + "." + f
		}

		// 1. Reserve the WHOLE group's slots (EEXIST => ordinal occupied).
		reserved := make(map[string]bool, len(s.cfg.Formats))
		reserveErr := ""
		reserveExist := false
		for _, f := range s.cfg.Formats {
			sf, err := os.OpenFile(filepath.Join(s.cfg.Dir, names[f]+".reserve"), os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				reserveErr = f + ": reserve slot: " + err.Error()
				reserveExist = os.IsExist(err)
				break
			}
			sf.Close()
			reserved[f] = true
		}
		if reserveErr != "" {
			for f := range reserved {
				os.Remove(filepath.Join(s.cfg.Dir, names[f]+".reserve"))
			}
			if reserveExist {
				continue // base ordinal occupied -> the whole group advances
			}
			return []string{reserveErr}
		}

		// 2. Per-format tmp writes (write failures never move the ordinal).
		writeErrs := make(map[string]error, len(s.cfg.Formats))
		for _, f := range s.cfg.Formats {
			if werr := s.writeTmp(filepath.Join(s.cfg.Dir, names[f]+".tmp"), f, res); werr != nil {
				writeErrs[f] = werr
			}
		}

		// 2b. Pre-check EVERY target BEFORE any link: if an external/legacy file
		// already occupies ANY format's target, the group advances immediately and
		// no link is performed at all — so the common collision case never
		// publishes and therefore never has to delete anything (R163/B).
		occupied := false
		linkCollide := false
		linkErrs := make(map[string]error)
		for _, f := range s.cfg.Formats {
			if writeErrs[f] != nil {
				continue
			}
			if _, err := os.Lstat(filepath.Join(s.cfg.Dir, names[f])); err == nil {
				occupied = true
				break
			} else if !os.IsNotExist(err) {
				linkErrs[f] = err // path unusable: report it, never guess
			}
		}

		// 3. No-replace links. After the pre-check, an EEXIST here can only be a
		// RACE (someone created the target between the check and the link).
		linked := make(map[string]os.FileInfo)
		if !occupied {
			for _, f := range s.cfg.Formats {
				if writeErrs[f] != nil || linkErrs[f] != nil {
					continue
				}
				target := filepath.Join(s.cfg.Dir, names[f])
				if lerr := os.Link(target+".tmp", target); lerr != nil {
					if os.IsExist(lerr) {
						linkCollide = true
						break
					}
					linkErrs[f] = lerr
					continue
				}
				// Ownership evidence: remember the EXACT file we published
				// (volume + file index), not merely its pathname, so a later
				// rollback can prove the path still IS our artifact.
				if info, serr := os.Lstat(target); serr == nil {
					linked[f] = info
				}
			}
		}

		// Cleanup tmp + sentinel debris for every format of this ordinal.
		for _, f := range s.cfg.Formats {
			os.Remove(filepath.Join(s.cfg.Dir, names[f]+".tmp"))
			os.Remove(filepath.Join(s.cfg.Dir, names[f]+".reserve"))
		}
		if occupied {
			// Nothing was linked at this ordinal: the whole group advances.
			// Never fall through to the success path here — that would report
			// success for a tick that published no file (P34-I2).
			continue
		}
		if linkCollide {
			// Roll back ONLY artifacts that are provably still OURS — never a
			// pathname that may have been replaced by another process (R163/B).
			for f, info := range linked {
				target := filepath.Join(s.cfg.Dir, names[f])
				if rerr := removeOwnArtifact(target, info); rerr != nil {
					linkErrs[f] = rerr
				}
			}
			continue // the whole group retries at the next ordinal
		}

		var errs []string
		for _, f := range s.cfg.Formats {
			switch {
			case writeErrs[f] != nil:
				errs = append(errs, f+": write tmp: "+writeErrs[f].Error())
			case linkErrs[f] != nil:
				errs = append(errs, f+": link snapshot: "+linkErrs[f].Error())
			}
		}
		return errs
	}
	return []string{"could not reserve a free snapshot slot after 100 retries"}
}

func (s *HistoryExportScheduler) writeTmp(tmpPath, fmtName string, res protection.TransitionReadResult) (err error) {
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
	}()
	// Stream straight into the file (no full-buffer copy); the serializer
	// writers are the same pure functions the HTTP export uses.
	if werr := serializeHistoryExportForFormat(f, fmtName, res); werr != nil {
		return werr
	}
	return f.Sync()
}

// removeOwnArtifact removes path ONLY IF it is still the exact file identified
// by want — the os.FileInfo captured when WE published it. os.SameFile compares
// the volume serial and file index, so a pathname that has since been replaced
// (deleted and recreated) by another process is recognised as foreign and is
// NEVER deleted; the caller gets an error instead and reports it (R163/B:
// rollback must be ownership-safe, not pathname-based). A path that is already
// gone is treated as success (there is nothing of ours left to remove).
func removeOwnArtifact(path string, want os.FileInfo) error {
	cur, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !os.SameFile(want, cur) {
		return fmt.Errorf("refusing to remove %s: no longer the artifact we published", path)
	}
	return os.Remove(path)
}

// serializeHistoryExportForFormat dispatches to the shared Phase 33 serializers
// (JSON/CSV). Centralized so the on-disk snapshot matches the HTTP export shape.
func serializeHistoryExportForFormat(w io.Writer, fmtName string, res protection.TransitionReadResult) error {
	switch fmtName {
	case "json":
		return serializeHistoryExportJSON(w, res)
	case "csv":
		return serializeHistoryExportCSV(w, res)
	default:
		return fmt.Errorf("unsupported format %q", fmtName)
	}
}

// collisionOrFormatRE matches a trailing collision-retry ordinal ".<digits>"
// OR format extension ".json"/".csv" on a snapshot file name.
var collisionOrFormatRE = regexp.MustCompile(`\.(?:\d+|json|csv)$`)

// prune enforces the bounded local retention cap (P34-I6) in SNAPSHOT units.
// 0 keeps all snapshots. Candidates are the scheduler's own snapshot files only
// (prefixed historyExportFileBase + "-"); .tmp/.reserve leftovers are ignored,
// and unrelated files in Dir are never touched. A snapshot is identified by its
// ExportedAt timestamp after stripping the format (.json/.csv) AND any trailing
// collision-retry suffix, so all artifact files of one export (and any rare
// collision variants) form a single retention unit removed together. Oldest
// excess snapshots are removed (P34-I6 corrected to snapshot units per R161/B).
func (s *HistoryExportScheduler) prune() error {
	if s.cfg.Retain <= 0 {
		return nil // 0 = keep all
	}
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("prune read dir: %w", err)
	}
	// group files by snapshot identity (one export = one unit, multi-format)
	groups := make(map[string][]string) // groupKey -> file names
	var order []string                   // insertion order of group keys
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, historyExportFileBase+"-") {
			continue
		}
		if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".reserve") {
			continue
		}
		gk := snapshotGroupKey(name)
		if _, ok := groups[gk]; !ok {
			order = append(order, gk)
		}
		groups[gk] = append(groups[gk], name)
	}
	if len(groups) <= s.cfg.Retain {
		return nil
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	excess := len(groups) - s.cfg.Retain
	for i := 0; i < excess; i++ {
		gk := order[i]
		for _, fn := range groups[gk] {
			if err := os.Remove(filepath.Join(s.cfg.Dir, fn)); err != nil {
				return fmt.Errorf("prune remove %s: %w", fn, err)
			}
		}
	}
	return nil
}

// snapshotGroupKey returns the retention unit (snapshot) identity for a
// snapshot file: alert-transitions-<ts>[.N].<fmt> -> "<ts>". It REPEATEDLY
// strips a trailing format suffix (.json/.csv) and trailing collision ordinal
// (".<digits>") in ANY order, so both the current base-level naming
// ("<ts>.1.json") and legacy per-format collision names ("<ts>.json.1") collapse
// into the same unit as the plain artifacts of that ExportedAt (R162/B fix —
// the previous strip-once ordering mis-grouped "<ts>.<fmt>.<N>" as "<ts>.<fmt>").
// The timestamp layout always ends in "Z", so the fractional ".<digits>" inside
// the timestamp itself is never stripped.
func snapshotGroupKey(name string) string {
	body := strings.TrimPrefix(name, historyExportFileBase+"-")
	for {
		next := collisionOrFormatRE.ReplaceAllString(body, "")
		if next == body {
			return body
		}
		body = next
	}
}

// Status returns a snapshot of the scheduler state for the read API.
func (s *HistoryExportScheduler) Status() HistoryExportStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := HistoryExportStatus{
		Enabled:   true,
		Running:   s.running,
		LastError: s.lastError,
		PruneError: s.pruneError,
		SkipCount: s.skipCount,
		Published: s.published,
		Failed:    s.failed,
		Dir:       s.cfg.Dir,
		Interval:  s.cfg.Interval.String(),
		Formats:   s.cfg.Formats,
		Retain:    s.cfg.Retain,
	}
	if !s.lastRunAt.IsZero() {
		t := s.lastRunAt
		st.LastRunAt = &t
	}
	if !s.lastExportedAt.IsZero() {
		t := s.lastExportedAt
		st.LastExportedAt = &t
	}
	return st
}

// Done returns a channel closed when the run loop has exited (parent cancelled
// and in-flight tick settled). Tests must wait on this — NOT on Start() (which
// is fire-and-forget, P34-I4-ARCH-2).
func (s *HistoryExportScheduler) Done() <-chan struct{} {
	return s.done
}
