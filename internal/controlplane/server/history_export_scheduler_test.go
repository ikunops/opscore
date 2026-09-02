package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// fakeExportStore is a controllable protection.AlertTransitionStore for
// scheduler tests. Its ReadAll returns a pre-set result (optionally after a
// delay to simulate a slow tick for the non-reentrant test).
type fakeExportStore struct {
	mu        sync.Mutex
	res       protection.TransitionReadResult
	delay     time.Duration
	readCalls int
}

func (f *fakeExportStore) Append(ctx context.Context, t protection.AlertTransition) error { return nil }
func (f *fakeExportStore) Load(ctx context.Context) protection.TransitionLoadResult {
	f.mu.Lock(); defer f.mu.Unlock(); return f.res
}
func (f *fakeExportStore) ReadRecent(ctx context.Context, n int) protection.TransitionReadResult {
	f.mu.Lock(); defer f.mu.Unlock(); return f.res
}
func (f *fakeExportStore) ReadBefore(ctx context.Context, cursor string, n int) protection.TransitionPageResult {
	return protection.TransitionPageResult{}
}
func (f *fakeExportStore) ReadAll(ctx context.Context) protection.TransitionReadResult {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return protection.TransitionReadResult{LoadErr: ctx.Err()}
		}
	}
	f.mu.Lock()
	f.readCalls++
	f.mu.Unlock()
	f.mu.Lock(); defer f.mu.Unlock()
	return f.res
}
func (f *fakeExportStore) Close() error { return nil }

func (f *fakeExportStore) calls() int {
	f.mu.Lock(); defer f.mu.Unlock(); return f.readCalls
}

func sampleTransitions() []protection.AlertTransition {
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return []protection.AlertTransition{
		{At: base, From: false, To: true, UnknownRate: 1, Threshold: 10},
		{At: base.Add(time.Second), From: true, To: false, UnknownRate: 2, Threshold: 20},
		{At: base.Add(2 * time.Second), From: false, To: true, UnknownRate: 3, Threshold: 30},
	}
}

// safeTS mirrors the scheduler's on-disk timestamp formatting so tests can
// predict the snapshot file name for a given ExportedAt.
func safeTS(t time.Time) string {
	return t.UTC().Format("20060102T150405.999999999") + "Z"
}

// formalFiles returns only the scheduler's own snapshot files (excludes .tmp
// and .reserve leftovers).
func formalFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(n, historyExportFileBase+"-") {
			continue
		}
		if strings.HasSuffix(n, ".tmp") || strings.HasSuffix(n, ".reserve") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// T1 — New validation: illegal configs fail fast; valid config builds.
func TestHistoryExportConfigValidation(t *testing.T) {
	good := HistoryExportConfig{Store: &fakeExportStore{}, Dir: t.TempDir(), Interval: time.Minute, Formats: []string{"json"}}
	if _, err := NewHistoryExportScheduler(good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := []struct {
		name string
		cfg  HistoryExportConfig
	}{
		{"zero interval", HistoryExportConfig{Dir: t.TempDir(), Interval: 0, Formats: []string{"json"}}},
		{"empty dir", HistoryExportConfig{Dir: "", Interval: time.Minute, Formats: []string{"json"}}},
		{"no formats", HistoryExportConfig{Dir: t.TempDir(), Interval: time.Minute, Formats: nil}},
		{"empty format entry", HistoryExportConfig{Dir: t.TempDir(), Interval: time.Minute, Formats: []string{""}}},
		{"unknown format", HistoryExportConfig{Dir: t.TempDir(), Interval: time.Minute, Formats: []string{"xml"}}},
		{"negative retain", HistoryExportConfig{Dir: t.TempDir(), Interval: time.Minute, Formats: []string{"json"}, Retain: -1}},
	}
	for _, c := range cases {
		c.cfg.Store = &fakeExportStore{}
		if _, err := NewHistoryExportScheduler(c.cfg); err == nil {
			t.Errorf("%s: expected fail-fast error, got nil", c.name)
		}
	}
}

// T2 — a single Tick writes the snapshot file with the expected name.
func TestHistoryExportSingleTickWritesFile(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 123456789, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, err := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())

	files := formalFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}
	want := "alert-transitions-" + safeTS(exp) + ".json"
	if files[0] != want {
		t.Fatalf("filename = %q, want %q", files[0], want)
	}
	// content parses as the export envelope
	data, _ := os.ReadFile(filepath.Join(dir, files[0]))
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("json parse: %v", err)
	}
	if env["schema"] != "alert-transition/export/v1" {
		t.Fatalf("schema = %v", env["schema"])
	}
	if tr, ok := env["transitions"].([]any); !ok || len(tr) != 3 {
		t.Fatalf("transitions = %v", env["transitions"])
	}
}

// T3 — both formats are materialized.
func TestHistoryExportBothFormats(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json", "csv"}})
	s.Tick(context.Background())

	files := formalFiles(t, dir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %v", files)
	}
	// each format present
	joined := strings.Join(files, " ")
	if !strings.Contains(joined, ".json") || !strings.Contains(joined, ".csv") {
		t.Fatalf("missing a format: %v", files)
	}
}

// T4/T7 — non-reentrant: a slow in-flight Tick rejects a concurrent Tick (skip).
func TestHistoryExportNonReentrant(t *testing.T) {
	dir := t.TempDir()
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: time.Now()}, delay: 100 * time.Millisecond}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.Tick(context.Background()) }()
	go func() { defer wg.Done(); s.Tick(context.Background()) }()
	wg.Wait()

	st.mu.Lock()
	calls := st.readCalls
	st.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly 1 ReadAll (one active tick), got %d", calls)
	}
	st.mu.Lock()
	skip := s.skipCount
	st.mu.Unlock()
	if skip != 1 {
		t.Fatalf("expected 1 skipped concurrent tick, got %d", skip)
	}
}

// T8 — ExportedAt provenance: file name uses the STORE's timestamp, not the
// scheduler clock (P34-CLOCK-1).
func TestHistoryExportUsesStoreExportedAt(t *testing.T) {
	dir := t.TempDir()
	storeTS := time.Date(2025, 1, 2, 3, 4, 5, 600000000, time.UTC) // fixed, injected
	schedClock := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC) // must NOT appear
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: storeTS}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{
		Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}, Clock: func() time.Time { return schedClock },
	})
	s.Tick(context.Background())

	files := formalFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}
	if !strings.Contains(files[0], safeTS(storeTS)) {
		t.Fatalf("filename %q does not contain store timestamp %q", files[0], safeTS(storeTS))
	}
	if strings.Contains(files[0], "2099") {
		t.Fatalf("filename leaked scheduler clock: %q", files[0])
	}
	// LastExportedAt in status equals storeTS (not schedClock)
	st2 := s.Status()
	if st2.LastExportedAt == nil || !st2.LastExportedAt.Equal(storeTS) {
		t.Fatalf("LastExportedAt = %v, want %v", st2.LastExportedAt, storeTS)
	}
}

// T5 (P34-I3) — same exported_at does not silently overwrite: a second tick with
// the same ts produces a distinct, renamed file; the first is untouched.
func TestHistoryExportSameTsNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})

	s.Tick(context.Background())
	first := filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".json")
	orig, _ := os.ReadFile(first)
	if len(orig) == 0 {
		t.Fatal("first snapshot empty")
	}

	s.Tick(context.Background()) // same ts -> must not overwrite first

	after, _ := os.ReadFile(first)
	if string(after) != string(orig) {
		t.Fatal("first snapshot was overwritten by second tick")
	}
	files := formalFiles(t, dir)
	// original + renamed retry (alert-transitions-<ts>.json.1)
	if len(files) != 2 {
		t.Fatalf("expected 2 files (original + renamed retry), got %v", files)
	}
	hasRetry := false
	for _, f := range files {
		if f == "alert-transitions-"+safeTS(exp)+".json.1" {
			hasRetry = true
		}
	}
	if !hasRetry {
		t.Fatalf("expected renamed retry file, got %v", files)
	}
}

// T6/T9 — partial failure: one format fails to publish -> explicit "partial
// export" error; the successful format's file is still written. Here the csv
// .tmp path is occupied by a directory so csv's writeTmp fails on the FIRST
// attempt (no retry loop), while json publishes normally.
func TestHistoryExportPartialFailure(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	// Occupy the csv .tmp path with a directory so its writeTmp fails.
	if err := os.Mkdir(filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".csv.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json", "csv"}})
	s.Tick(context.Background())

	st2 := s.Status()
	if st2.Published != 1 {
		t.Fatalf("published = %d, want 1 (json ok)", st2.Published)
	}
	if st2.Failed != 1 {
		t.Fatalf("failed = %d, want 1 (csv write failed)", st2.Failed)
	}
	if !strings.Contains(st2.LastError, "partial export") {
		t.Fatalf("LastError = %q, want 'partial export'", st2.LastError)
	}
	// json file exists; csv never published (no overwrite, no partial file).
	files := formalFiles(t, dir)
	if len(files) != 1 || files[0] != "alert-transitions-"+safeTS(exp)+".json" {
		t.Fatalf("expected only json snapshot, got %v", files)
	}
}

// T10 — lifecycle: an in-flight tick settles on ctx cancel, run loop exits,
// Done() closes. Deterministic under load: we sync on a tick START (the fake
// store's delay keeps one ReadAll observably in-flight), then cancel. The
// in-flight ReadAll respects ctx and returns early (LoadErr), so Tick returns
// without publishing and run() sees ctx.Done and closes Done() promptly — no
// dependence on file-I/O timing or scheduler preemption (which made a fixed
// 2s wait flaky when the full suite runs packages in parallel).
func TestHistoryExportShutdownWaitsDone(t *testing.T) {
	dir := t.TempDir()
	st := &fakeExportStore{
		res:   protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: time.Now()},
		delay: 50 * time.Millisecond, // keep a tick observably in-flight at cancel time
	}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: 5 * time.Millisecond, Formats: []string{"json"}})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	// Synchronous point: wait until at least one tick has entered ReadAll.
	started := make(chan struct{})
	go func() {
		for st.calls() == 0 {
			time.Sleep(2 * time.Millisecond)
		}
		close(started)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler never started a tick")
	}

	cancel() // in-flight tick's ReadAll returns ctx.Err(); Tick returns; run() exits

	select {
	case <-s.Done():
		// OK — run loop has exited (no new ticks scheduled after cancel)
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not report Done after ctx cancel")
	}
}

// T11 — Start is idempotent: a second Start does NOT spawn a new run loop.
func TestHistoryExportStartOnce(t *testing.T) {
	dir := t.TempDir()
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: time.Now()}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: 2 * time.Millisecond, Formats: []string{"json"}})

	ctx1, cancel1 := context.WithCancel(context.Background())
	s.Start(ctx1)
	time.Sleep(30 * time.Millisecond)
	cancel1()
	<-s.Done()

	publishedAfterFirst := s.Status().Published

	// Second Start must be a no-op: no new run loop, so published must NOT grow.
	ctx2, cancel2 := context.WithCancel(context.Background())
	s.Start(ctx2)
	time.Sleep(30 * time.Millisecond)
	cancel2()
	_ = ctx2
	_ = cancel2

	if got := s.Status().Published; got != publishedAfterFirst {
		t.Fatalf("second Start spawned a new loop (published %d -> %d); expected no-op", publishedAfterFirst, got)
	}
}

// T12 — failure before publication leaves NO half-written formal file (only a
// temp/reserve may linger). Here writeTmp fails because a directory occupies the
// .tmp path.
func TestHistoryExportNoFormalFileOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})

	// Occupy the .tmp path with a directory so writeTmp's OpenFile fails.
	tmpDir := filepath.Join(dir, "alert-transitions-"+safeTS(exp)+".json.tmp")
	if err := os.Mkdir(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	s.Tick(context.Background())

	files := formalFiles(t, dir)
	if len(files) != 0 {
		t.Fatalf("expected NO formal file after write failure, got %v", files)
	}
}

// T13 (no-replace, external/legacy file) — a pre-existing snapshot with the same
// base name is NEVER overwritten; the scheduler publishes a renamed candidate.
func TestHistoryExportNoReplaceExternalFile(t *testing.T) {
	dir := t.TempDir()
	exp := time.Date(2026, 8, 29, 13, 45, 30, 0, time.UTC)
	// External/legacy file already occupying the target name.
	legacyName := "alert-transitions-" + safeTS(exp) + ".json"
	legacyPath := filepath.Join(dir, legacyName)
	if err := os.WriteFile(legacyPath, []byte("LEGACY-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions(), ExportedAt: exp}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())

	// Legacy must be byte-for-byte intact.
	legacy, _ := os.ReadFile(legacyPath)
	if string(legacy) != "LEGACY-CONTENT" {
		t.Fatalf("legacy file overwritten: %q", legacy)
	}
	// A renamed candidate must exist with real content.
	files := formalFiles(t, dir)
	hasRetry := false
	for _, f := range files {
		if f == legacyName+".1" {
			hasRetry = true
			data, _ := os.ReadFile(filepath.Join(dir, f))
			var env map[string]any
			if err := json.Unmarshal(data, &env); err != nil {
				t.Fatalf("retry file not valid export: %v", err)
			}
		}
	}
	if !hasRetry {
		t.Fatalf("expected renamed candidate file, got %v", files)
	}
}

// T14 — prune enforces bounded local retention (P34-I6). 0 keeps all.
func TestHistoryExportPruneRetention(t *testing.T) {
	dir := t.TempDir()
	st := &fakeExportStore{res: protection.TransitionReadResult{Transitions: sampleTransitions()}}
	// Retain=3: write 5 distinct snapshots (distinct ExportedAt), expect 3 kept.
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}, Retain: 3})
	for i := 0; i < 5; i++ {
		st.res.ExportedAt = time.Date(2026, 8, 29, 13, 0, 0, i*1000, time.UTC)
		s.Tick(context.Background())
	}
	if got := len(formalFiles(t, dir)); got != 3 {
		t.Fatalf("after prune expected 3 files, got %d", got)
	}

	// Retain=0 keeps all.
	dir2 := t.TempDir()
	s2, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir2, Interval: time.Hour, Formats: []string{"json"}, Retain: 0})
	for i := 0; i < 5; i++ {
		st.res.ExportedAt = time.Date(2026, 8, 29, 14, 0, 0, i*1000, time.UTC)
		s2.Tick(context.Background())
	}
	if got := len(formalFiles(t, dir2)); got != 5 {
		t.Fatalf("retain=0 should keep all, got %d", got)
	}
}

// T15 — durable-only skip: nil store marks skip, writes no file.
func TestHistoryExportNilStoreSkips(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: nil, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())
	if len(formalFiles(t, dir)) != 0 {
		t.Fatal("nil store must not write any file")
	}
	if s.Status().LastError == "" {
		t.Fatal("nil store skip must set LastError")
	}
}

// T16 — read error / corrupt: no file written, failure recorded.
func TestHistoryExportReadErrorNoFile(t *testing.T) {
	dir := t.TempDir()
	st := &fakeExportStore{res: protection.TransitionReadResult{LoadErr: context.DeadlineExceeded}}
	s, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st, Dir: dir, Interval: time.Hour, Formats: []string{"json"}})
	s.Tick(context.Background())
	if len(formalFiles(t, dir)) != 0 {
		t.Fatal("read error must not write a file")
	}
	if s.Status().Failed != 1 {
		t.Fatalf("failed = %d, want 1", s.Status().Failed)
	}

	dir2 := t.TempDir()
	st2 := &fakeExportStore{res: protection.TransitionReadResult{Corrupt: true}}
	s2, _ := NewHistoryExportScheduler(HistoryExportConfig{Store: st2, Dir: dir2, Interval: time.Hour, Formats: []string{"json"}})
	s2.Tick(context.Background())
	if len(formalFiles(t, dir2)) != 0 {
		t.Fatal("corrupt must not write a file")
	}
}

// itoa is a tiny local helper (avoid importing strconv in tests just for this).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

var _ = itoa // retained for potential future collision tests
