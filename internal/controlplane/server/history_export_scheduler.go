package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// historyExportFileBase is the on-disk snapshot prefix (Phase 34). Files are
// named alert-transitions-<ts>.<fmt>[.N]; the "[.N]" suffix is a collision
// retry index, never present in the common case.
const historyExportFileBase = "alert-transitions"

// HistoryExportConfig configures the Phase 34 scheduled periodic export of the
// durable alert-transition history to local files. The scheduler is OPT-IN: the
// composition root only constructs it when BOTH a positive interval and a
// destination dir are supplied; otherwise it stays nil and nothing is written.
type HistoryExportConfig struct {
	// Store is the durable alert-transition store. May be nil (memory mode):
	// a nil store makes every tick a durable-unavailable SKIP (never a
	// pseudo-success file), not a startup error (P34 durable-only).
	Store protection.AlertTransitionStore
	// Dir is the destination directory for snapshot files. Required when enabled.
	Dir string
	// Interval is the tick period. Required > 0 when enabled.
	Interval time.Duration
	// Formats lists the export encodings to materialize each tick. Each format
	// is published independently; a partial failure is reported explicitly
	// (P34-I2 — no faked success). Only "json" and "csv" are accepted.
	Formats []string
	// Retain is the local file retention cap. 0 keeps ALL snapshots; >0 keeps
	// at most Retain newest files (P34-I6 bounded local retention). Default 96.
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

	// Durable-only (P34 durable-only): a nil store means memory mode — there is
	// no durable history to export, so we SKIP and mark it explicitly. We never
	// write a pseudo-success file containing the 256-ring snapshot.
	if s.cfg.Store == nil {
		s.mu.Lock()
		s.lastError = "durable store not configured (memory mode) — skipped"
		s.mu.Unlock()
		return
	}

	res := s.cfg.Store.ReadAll(ctx)
	if res.LoadErr != nil {
		s.mu.Lock()
		s.lastError = "read error: " + res.LoadErr.Error()
		s.failed++
		s.mu.Unlock()
		return
	}
	if res.Corrupt {
		s.mu.Lock()
		s.lastError = "durable history corrupt — skipped"
		s.failed++
		s.mu.Unlock()
		return
	}

	// Publish each format independently. Per-format success is the only thing
	// that counts as a success; partial failure is reported explicitly, never
	// masked as a full success (P34-I2).
	var pubErrs []string
	anyOK := false
	for _, f := range s.cfg.Formats {
		if err := s.publishFormat(res, f); err != nil {
			pubErrs = append(pubErrs, f+": "+err.Error())
		} else {
			anyOK = true
		}
	}

	s.mu.Lock()
	switch {
	case len(pubErrs) == 0:
		// Full success: record provenance, clear any prior error.
		s.lastExportedAt = res.ExportedAt
		s.lastError = ""
		s.published++
	case anyOK:
		// Partial success: proveance recorded; mark explicit partial error.
		s.lastExportedAt = res.ExportedAt
		s.lastError = "partial export: " + strings.Join(pubErrs, "; ")
		s.published++
		s.failed++
	default:
		// Total failure: no file was published.
		s.lastError = "export failed: " + strings.Join(pubErrs, "; ")
		s.failed++
	}
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

// publishFormat atomically materializes one format to disk with a no-replace
// guarantee (P34-I3). Scheme:
//  1. reserve a unique slot via a sentinel file (O_CREATE|O_EXCL) — exists-check
//     and reservation are one syscall, so there is no TOCTOU window;
//  2. write content to a .tmp file, Sync, Close;
//  3. os.Link(tmp -> target). Link is no-replace: if target already exists the
//     OS returns EEXIST. On EEXIST we derive a new candidate base name and retry
//     — we NEVER overwrite an existing snapshot (ours, an external process's, or
//     a legacy export). On ANY failure we delete the .tmp and sentinel and leave
//     no half-written formal file.
// A crash between steps 2 and 3 leaves only a .tmp + sentinel; never a corrupt
// formal file, and never a silently-overwritten one.
func (s *HistoryExportScheduler) publishFormat(res protection.TransitionReadResult, fmtName string) error {
	// Filesystem-safe timestamp: RFC3339Nano has ':' which is Windows-invalid,
	// so use a colon-free layout. res.ExportedAt is store provenance (P34-CLOCK-1).
	safe := res.ExportedAt.UTC().Format("20060102T150405.999999999") + "Z"
	base := fmt.Sprintf("%s-%s.%s", historyExportFileBase, safe, fmtName)

	for attempt := 0; attempt < 100; attempt++ {
		cand := base
		if attempt > 0 {
			cand = fmt.Sprintf("%s.%d", base, attempt)
		}
		sentinelPath := filepath.Join(s.cfg.Dir, cand+".reserve")
		targetPath := filepath.Join(s.cfg.Dir, cand)
		tmpPath := targetPath + ".tmp"

		// 1. Reserve the slot atomically (EEXIST => another publisher or a
		// leftover reserve holds this name; move to the next candidate).
		sf, err := os.OpenFile(sentinelPath, os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("reserve slot: %w", err)
		}
		sf.Close()

		// 2. Write tmp (Sync ensures durability before Link).
		if werr := s.writeTmp(tmpPath, fmtName, res); werr != nil {
			os.Remove(sentinelPath)
			return werr
		}

		// 3. No-replace publish.
		if lerr := os.Link(tmpPath, targetPath); lerr != nil {
			os.Remove(tmpPath)
			os.Remove(sentinelPath)
			if os.IsExist(lerr) {
				// An external/legacy file already occupies targetPath. Never
				// overwrite it; try the next candidate base name.
				continue
			}
			return fmt.Errorf("link snapshot: %w", lerr)
		}
		os.Remove(tmpPath)
		os.Remove(sentinelPath)
		return nil
	}
	return fmt.Errorf("could not reserve a free snapshot slot after 100 retries")
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

// prune enforces the bounded local retention cap (P34-I6). 0 keeps all files.
// Candidates are the scheduler's own snapshot files only (prefixed
// historyExportFileBase + "-"); .tmp/.reserve leftovers are ignored, and
// unrelated files in Dir are never touched. Oldest excess files are removed.
func (s *HistoryExportScheduler) prune() error {
	if s.cfg.Retain <= 0 {
		return nil // 0 = keep all
	}
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("prune read dir: %w", err)
	}
	type snap struct {
		name string
		key  string // chronological sort key (timestamp (+ optional .N suffix))
	}
	var snaps []snap
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
		snaps = append(snaps, snap{name: name, key: snapshotSortKey(name)})
	}
	if len(snaps) <= s.cfg.Retain {
		return nil
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].key < snaps[j].key })
	excess := len(snaps) - s.cfg.Retain
	for i := 0; i < excess; i++ {
		if err := os.Remove(filepath.Join(s.cfg.Dir, snaps[i].name)); err != nil {
			return fmt.Errorf("prune remove %s: %w", snaps[i].name, err)
		}
	}
	return nil
}

// snapshotSortKey extracts the chronological ordering key from a snapshot file
// name: alert-transitions-<ts>.<fmt>[.N] -> "<ts>[.N]". Stripping the format
// suffix keeps json/csv snapshots at the same timestamp ordered together.
func snapshotSortKey(name string) string {
	const prefix = historyExportFileBase + "-"
	if !strings.HasPrefix(name, prefix) {
		return name
	}
	body := strings.TrimPrefix(name, prefix)
	for _, ext := range []string{"json", "csv"} {
		if strings.HasSuffix(body, "."+ext) {
			body = strings.TrimSuffix(body, "."+ext)
			break
		}
	}
	return body
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
