package server

// T390/T391 (ADR-085/086, Phase 53): the host-registry capability cache. A
// successful probe publishes its snapshot hash into the named host's labels;
// a FAILED probe falls back to that last-known snapshot (Source=SourceCache).
// The tests below drive the HTTP face the operators actually use
// (GET /api/inventory/{name}) against a named host whose SSH port is closed,
// so the fallback path is exercised without an SSH server.
//
// The label name is pinned as a literal on purpose: it is the wire contract
// between the registry and the cache, visible through GET /api/hosts/{name}.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/builtin/capability"
	"github.com/YuDong999/opscore/internal/controlplane/hostregistry"
	"github.com/YuDong999/opscore/internal/controlplane/sync"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
	"github.com/YuDong999/opscore/internal/storage"
)

// testHintLabel is the observable label key carrying a host's last-known
// capability snapshot hash (ADR-085 §3.1).
const testHintLabel = "capability_snapshot_hash"

// newCapCacheTestServer builds a server with the Host Registry configured and
// the capability cache switch in the requested position. The SSH target points
// at 127.0.0.1:1 so every probe is refused immediately (no SSH server needed).
func newCapCacheTestServer(t *testing.T, enabled bool, hosts hostregistry.HostStore) (*Server, storage.Storage) {
	t.Helper()
	stor := storage.NewMemoryStorage()
	registry := core.NewRegistry()
	registry.Register(core.Operation{
		Name:       "system.host.capability.list",
		Permission: core.Permission{ResourceType: "system.host", Action: "read"},
		Risk:       core.RiskLow,
		Handler:    capability.NewListHandler(),
	})
	dispatcher := core.NewDispatcher(registry, core.NewExecutor(core.NewLogSink(slog.Default())))
	if err := sync.New(registry, stor).Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	srv, err := New(Config{
		Storage:              stor,
		Dispatcher:           dispatcher,
		HostStore:            hosts,
		CapabilityCacheHints: enabled,
		AccessSecret:         "access-secret",
		RefreshSecret:        "refresh-secret",
		AllowRegister:        true,
		BootstrapAdmin:       &BootstrapAdmin{Username: "admin", Password: "adminpw"},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv, stor
}

// refusedTarget is a connection spec whose probe always fails fast.
func refusedTarget() core.TargetHost {
	return core.TargetHost{Address: "127.0.0.1", Port: 1, User: "root"}
}

// seedLastKnownSnapshot persists a snapshot and returns its content hash — the
// exact value a previous successful probe would have published as the hint.
func seedLastKnownSnapshot(t *testing.T, stor storage.Storage, systemdAvailable bool) string {
	t.Helper()
	snap := &snapshot.CapabilitySnapshot{
		HostID:      "web-01",
		Version:     1,
		Items:       map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: systemdAvailable}},
		CollectedAt: time.Now().Add(-time.Hour).UTC(),
		Source:      snapshot.SourceSSH,
	}
	if _, err := storage.PersistCapabilitySnapshot(snap, stor.Snapshots()); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
	return snap.Hash()
}

// T390: with the cache enabled, a refused probe serves the last-known snapshot
// with Source=cache — the resolver gets facts instead of a guess.
func TestT390CapabilityCacheFallbackServesLastKnown(t *testing.T) {
	hosts := hostregistry.NewMemoryHostStore()
	srv, stor := newCapCacheTestServer(t, true, hosts)
	hash := seedLastKnownSnapshot(t, stor, true)
	if _, err := hosts.Save(hostregistry.Host{
		Name:   "web-01",
		Target: refusedTarget(),
		Labels: map[string]string{testHintLabel: hash},
	}); err != nil {
		t.Fatalf("save host: %v", err)
	}

	h := srv.Handler()
	tok := login(t, h, "admin", "adminpw")
	rec := doJSON(t, h, "GET", "/api/inventory/web-01", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inventory status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"source":"cache"`) {
		t.Fatalf("T390: refused probe must serve the last-known snapshot as cache, got %s", body)
	}
	if !strings.Contains(body, `"systemd"`) {
		t.Fatalf("T390: cached capability set must be served, got %s", body)
	}
}

// T391: the cache fails closed to today's behaviour and never leaks across
// hosts. Four independent ways to be wrong:
//
//	a) switch off          -> no replay even though a hint exists (zero change)
//	b) hint dangling       -> hash not in the store: no replay, no panic
//	c) payload corrupt     -> no partial/garbage snapshot, no panic
//	d) another host's hint -> B's request never serves A's capability set
func TestT391CapabilityCacheFailClosedAndIsolated(t *testing.T) {
	// (a) switch off.
	hostsOff := hostregistry.NewMemoryHostStore()
	srvOff, storOff := newCapCacheTestServer(t, false, hostsOff)
	hashOff := seedLastKnownSnapshot(t, storOff, true)
	if _, err := hostsOff.Save(hostregistry.Host{Name: "web-01", Target: refusedTarget(),
		Labels: map[string]string{testHintLabel: hashOff}}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	hOff := srvOff.Handler()
	tokOff := login(t, hOff, "admin", "adminpw")
	if rec := doJSON(t, hOff, "GET", "/api/inventory/web-01", tokOff, nil); strings.Contains(rec.Body.String(), `"source":"cache"`) {
		t.Fatalf("T391a: disabled switch must not replay, got %s", rec.Body.String())
	}

	// (b) dangling hash and (c) corrupt payload, on one enabled server.
	hostsOn := hostregistry.NewMemoryHostStore()
	srvOn, storOn := newCapCacheTestServer(t, true, hostsOn)
	if _, err := storOn.Snapshots().Put("deadbeefdeadbeef", []byte("{not-json"), 1); err != nil {
		t.Fatalf("put corrupt payload: %v", err)
	}
	// (e) a WELL-FORMED payload stored under a reference that is not its
	// content hash: the reference must not resolve (content-addressed honesty).
	rawSnap, err := json.Marshal(&snapshot.CapabilitySnapshot{
		Items:  map[string]snapshot.CapabilityInfo{"nope": {Name: "nope", Available: true}},
		Source: snapshot.SourceSSH,
	})
	if err != nil {
		t.Fatalf("marshal mismatch payload: %v", err)
	}
	env, err := json.Marshal(storage.SnapshotEnvelope{SchemaVersion: storage.CapabilitySnapshotSchemaVersion, Payload: rawSnap})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if _, err := storOn.Snapshots().Put("cafecafecafecafe", env, storage.CapabilitySnapshotSchemaVersion); err != nil {
		t.Fatalf("put mismatched payload: %v", err)
	}
	if _, err := hostsOn.Save(hostregistry.Host{Name: "dangling", Target: refusedTarget(),
		Labels: map[string]string{testHintLabel: "0123456789abcdef"}}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	if _, err := hostsOn.Save(hostregistry.Host{Name: "corrupt", Target: refusedTarget(),
		Labels: map[string]string{testHintLabel: "deadbeefdeadbeef"}}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	if _, err := hostsOn.Save(hostregistry.Host{Name: "mismatch", Target: refusedTarget(),
		Labels: map[string]string{testHintLabel: "cafecafecafecafe"}}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	// (d) host A carries a VALID hint; host B carries none.
	hashA := seedLastKnownSnapshot(t, storOn, true)
	if _, err := hostsOn.Save(hostregistry.Host{Name: "web-01", Target: refusedTarget(),
		Labels: map[string]string{testHintLabel: hashA}}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	if _, err := hostsOn.Save(hostregistry.Host{Name: "web-02", Target: refusedTarget()}); err != nil {
		t.Fatalf("save host: %v", err)
	}

	hOn := srvOn.Handler()
	tokOn := login(t, hOn, "admin", "adminpw")
	for _, name := range []string{"dangling", "corrupt", "mismatch", "web-02"} {
		rec := doJSON(t, hOn, "GET", "/api/inventory/"+name, tokOn, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("T391: %s status = %d body=%s", name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"source":"cache"`) {
			t.Fatalf("T391(%s): must fail closed to the guess path, got %s", name, rec.Body.String())
		}
	}

	// The valid hint still works on the same server — proving (b)/(c)/(d) are
	// the hint's verdict, not a blanket disable.
	if rec := doJSON(t, hOn, "GET", "/api/inventory/web-01", tokOn, nil); !strings.Contains(rec.Body.String(), `"source":"cache"`) {
		t.Fatalf("T391 control: web-01's valid hint must still replay, got %s", rec.Body.String())
	}
}

// T393 (ADR-085 §3, defect D regression shape): an unreachable NAMED host with
// no cache hint must NOT be reported with the control plane's own identity or
// capabilities. Before the fix, Build() seeded the local snapshot under the
// remote target's key, the enrich guard short-circuited, and the inventory
// served local facts attributed to the named host.
func TestT393UnreachableNamedHostIsNotAttributedLocalFacts(t *testing.T) {
	hosts := hostregistry.NewMemoryHostStore()
	srv, _ := newCapCacheTestServer(t, false, hosts)
	if _, err := hosts.Save(hostregistry.Host{Name: "web-01", Target: refusedTarget()}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	h := srv.Handler()
	tok := login(t, h, "admin", "adminpw")
	rec := doJSON(t, h, "GET", "/api/inventory/web-01", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inventory status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"source":"local"`) {
		t.Fatalf("T393: local observation attributed to a remote target: %s", body)
	}
	// The control plane's own host identity must not appear either.
	if localID := core.NewContext().Build().HostSnapshot().ID; localID != "" && strings.Contains(body, localID) {
		t.Fatalf("T393: control-plane host identity leaked into the target's view: %s", body)
	}
}

// countingHostStore wraps a HostStore and counts Save calls, so the publish
// seam's "write only when the hash changed" claim is observable.
type countingHostStore struct {
	hostregistry.HostStore
	saves int
}

func (c *countingHostStore) Save(h hostregistry.Host) (hostregistry.Host, error) {
	c.saves++
	return c.HostStore.Save(h)
}

// T389 (ADR-086 §4): the cache seam, exercised directly (same package):
//   - disabled switch: no publish, no replay (zero change)
//   - enabled: the label carries the content hash; re-publishing the SAME hash
//     performs no second write (idempotent)
//   - unnamed targets and unknown host names are silent no-ops (best-effort)
//   - fresh-observation priority: a context already carrying a probe-sourced
//     snapshot is never replaced by the cached one
func TestT389CapabilityCacheHintPublishSeam(t *testing.T) {
	remote := refusedTarget()
	base := core.NewContext().Build()
	fresh := &snapshot.CapabilitySnapshot{
		Items:  map[string]snapshot.CapabilityInfo{"systemd": {Name: "systemd", Available: true}},
		Source: snapshot.SourceSSH,
	}
	withFresh := func() core.Context {
		child := core.WithTarget(base, remote)
		child.SetSnapshot(remote, fresh)
		return child
	}

	// Disabled: no publish, and no replay even for a snapshot-less context.
	hostsOff := &countingHostStore{HostStore: hostregistry.NewMemoryHostStore()}
	srvOff, _ := newCapCacheTestServer(t, false, hostsOff)
	if _, err := hostsOff.Save(hostregistry.Host{Name: "web-01", Target: remote}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	before := hostsOff.saves
	_ = srvOff.applyCapabilityCacheHints(withFresh(), "web-01", remote)
	if hostsOff.saves != before {
		t.Fatal("T389: disabled switch must not write the hint")
	}
	if h, _ := hostsOff.GetByName("web-01"); len(h.Labels) != 0 {
		t.Fatalf("T389: disabled switch must not set labels, got %v", h.Labels)
	}

	// Enabled: publish once, idempotent on the second call.
	hostsOn := &countingHostStore{HostStore: hostregistry.NewMemoryHostStore()}
	srvOn, _ := newCapCacheTestServer(t, true, hostsOn)
	if _, err := hostsOn.Save(hostregistry.Host{Name: "web-01", Target: remote}); err != nil {
		t.Fatalf("save host: %v", err)
	}
	savesBefore := hostsOn.saves
	_ = srvOn.applyCapabilityCacheHints(withFresh(), "web-01", remote)
	if hostsOn.saves != savesBefore+1 {
		t.Fatalf("T389: first publish must write exactly once (saves %d -> %d)", savesBefore, hostsOn.saves)
	}
	h, _ := hostsOn.GetByName("web-01")
	if h.Labels[testHintLabel] != fresh.Hash() {
		t.Fatalf("T389: label = %q, want %q", h.Labels[testHintLabel], fresh.Hash())
	}
	wrote := hostsOn.saves
	_ = srvOn.applyCapabilityCacheHints(withFresh(), "web-01", remote)
	if hostsOn.saves != wrote {
		t.Fatalf("T389: re-publishing the same hash must not write again (saves %d -> %d)", wrote, hostsOn.saves)
	}

	// Unnamed target: no write.
	savesBefore = hostsOn.saves
	_ = srvOn.applyCapabilityCacheHints(withFresh(), "", remote)
	// Unknown host name: silent no-op, no panic.
	_ = srvOn.applyCapabilityCacheHints(withFresh(), "ghost", remote)
	if hostsOn.saves != savesBefore {
		t.Fatal("T389: unnamed/unknown hosts must not write")
	}

	// Fresh priority: the context's probe-sourced snapshot survives even when a
	// (different, valid) cached hint exists for the host.
	cachedItems := &snapshot.CapabilitySnapshot{
		Items:  map[string]snapshot.CapabilityInfo{"apk": {Name: "apk", Available: true}},
		Source: snapshot.SourceSSH,
	}
	if _, err := storage.PersistCapabilitySnapshot(cachedItems, srvOn.stor.Snapshots()); err != nil {
		t.Fatalf("persist cached: %v", err)
	}
	h.Labels[testHintLabel] = cachedItems.Hash()
	if _, err := hostsOn.Save(h); err != nil {
		t.Fatalf("save host: %v", err)
	}
	got := srvOn.applyCapabilityCacheHints(withFresh(), "web-01", remote)
	if got.CapabilitySnapshot() != fresh {
		t.Fatal("T389: a fresh observation must never be replaced by the cached set")
	}
}
