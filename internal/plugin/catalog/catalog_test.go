package catalog

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

// fakeProvider is an in-memory manifest.Provider for catalog tests.
type fakeProvider struct {
	entries map[string]*manifest.Manifest
	listErr error
}

func (f *fakeProvider) List() ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	keys := make([]string, 0, len(f.entries))
	for k := range f.entries {
		keys = append(keys, k)
	}
	return keys, nil
}

func (f *fakeProvider) Read(key string) (*manifest.Manifest, error) {
	m, ok := f.entries[key]
	if !ok {
		return nil, fmt.Errorf("unknown key %q", key)
	}
	if m == nil {
		return nil, fmt.Errorf("malformed manifest %q", key)
	}
	return m, nil
}

func mf(name, version string, ops ...manifest.OperationDecl) *manifest.Manifest {
	return &manifest.Manifest{
		Name:          name,
		Version:       version,
		Operations:    ops,
		PluginAPI:     "opscore.plugin/v1",
		MinKernel:     "0.2.0",
		SchemaVersion: 1,
		Capabilities:  []string{"linux"},
	}
}

func op(name, res, act, risk string) manifest.OperationDecl {
	return manifest.OperationDecl{Name: name, Resource: res, Action: act, Risk: risk}
}

func testCatalog() *Catalog {
	fileSrc := &fakeProvider{entries: map[string]*manifest.Manifest{
		"mysql-1.0": mf("mysql", "1.0", op("plugin.mysql.db.list", "db", "list", "low")),
		"mysql-1.1": mf("mysql", "1.1", op("plugin.mysql.db.list", "db", "list", "low")),
		"broken":    nil, // malformed: must be skipped, not fatal
		"sysd-2.0":  mf("systemd", "2.0", op("system.service.restart", "service", "restart", "high")),
	}}
	ociSrc := &fakeProvider{entries: map[string]*manifest.Manifest{
		"mysql-2.0": mf("mysql", "2.0", op("plugin.mysql.db.dump", "db", "dump", "medium")),
	}}
	return New(Source{Name: "file", Provider: fileSrc}, Source{Name: "oci", Provider: ociSrc})
}

func TestListAggregatesSourcesAndSkipsMalformed(t *testing.T) {
	c := testCatalog()
	all, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// 4 valid entries; the malformed one must be skipped silently.
	if len(all) != 4 {
		t.Fatalf("want 4 entries, got %d: %+v", len(all), all)
	}
	// Deterministic order: mysql 1.0, 1.1, 2.0, then systemd 2.0.
	want := []string{"mysql/1.0", "mysql/1.1", "mysql/2.0", "systemd/2.0"}
	for i, w := range want {
		got := all[i].ID + "/" + all[i].Version
		if got != w {
			t.Errorf("entry %d: want %s, got %s", i, w, got)
		}
	}
	// Provenance must be recorded.
	if all[2].Source != "oci" {
		t.Errorf("mysql 2.0 should come from oci, got %q", all[2].Source)
	}
}

// --- Round 27 SHOULD-1: pagination -----------------------------------------

func TestSearchPagination(t *testing.T) {
	c := testCatalog()

	page, err := c.SearchPage(Query{Limit: 2})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if page.Total != 4 {
		t.Errorf("Total must count matches BEFORE windowing, want 4, got %d", page.Total)
	}
	if len(page.Items) != 2 {
		t.Fatalf("want 2 items on page 1, got %d", len(page.Items))
	}
	if got := page.Items[0].Version; got != "1.0" {
		t.Errorf("page 1 should start at mysql 1.0, got %s", got)
	}

	page2, err := c.SearchPage(Query{Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if len(page2.Items) != 2 || page2.Items[0].Version != "2.0" {
		t.Errorf("page 2 should start at mysql 2.0, got %+v", page2.Items)
	}

	// Past-the-end offset yields an empty page, never a panic.
	empty, err := c.SearchPage(Query{Offset: 999, Limit: 10})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if len(empty.Items) != 0 || empty.Total != 4 {
		t.Errorf("past-the-end offset: want 0 items / total 4, got %d / %d",
			len(empty.Items), empty.Total)
	}

	// Limit 0 means "no cap", preserving the unpaginated contract.
	uncapped, err := c.Search(Query{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(uncapped) != 4 {
		t.Errorf("Limit 0 must not cap results, got %d", len(uncapped))
	}
}

// --- Round 27 SHOULD-2: content digest --------------------------------------

func TestDigestIsStableAndContentAddressed(t *testing.T) {
	same1 := fromManifest(mf("mysql", "1.0"), Source{Name: "file"}, "k")
	same2 := fromManifest(mf("mysql", "1.0"), Source{Name: "oci"}, "other-key")
	if same1.Digest == "" {
		t.Fatal("digest must be populated")
	}
	if !strings.HasPrefix(same1.Digest, "sha256:") {
		t.Errorf("digest must be algorithm-prefixed, got %q", same1.Digest)
	}
	// Same manifest content => same digest, regardless of source/key. That is
	// what makes it usable for cache / diff / sync across sources.
	if same1.Digest != same2.Digest {
		t.Errorf("identical content must share a digest: %s vs %s", same1.Digest, same2.Digest)
	}
	// Different content => different digest.
	other := fromManifest(mf("mysql", "1.1"), Source{Name: "file"}, "k")
	if other.Digest == same1.Digest {
		t.Error("different content must not share a digest")
	}
}

// --- Round 27 SHOULD-3: source priority -------------------------------------

func TestSourcePriorityOrdersDuplicateEntries(t *testing.T) {
	community := &fakeProvider{entries: map[string]*manifest.Manifest{
		"mysql-1.0": mf("mysql", "1.0"),
	}}
	official := &fakeProvider{entries: map[string]*manifest.Manifest{
		"mysql-1.0": mf("mysql", "1.0"),
	}}
	// "community" sorts before "official" alphabetically, so if priority were
	// ignored this test would fail — it pins the priority-first ordering.
	c := New(
		Source{Name: "community", Provider: community, Priority: 10},
		Source{Name: "official", Provider: official, Priority: 0},
	)
	all, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 entries, got %d", len(all))
	}
	if all[0].Source != "official" {
		t.Errorf("lower Priority must sort first, got %q then %q", all[0].Source, all[1].Source)
	}
	if all[0].SourcePriority != 0 || all[1].SourcePriority != 10 {
		t.Errorf("SourcePriority must be projected onto entries: %d / %d",
			all[0].SourcePriority, all[1].SourcePriority)
	}
}

func TestListReportsSourceFailure(t *testing.T) {
	c := New(Source{Name: "git", Provider: &fakeProvider{listErr: fmt.Errorf("clone failed")}})
	if _, err := c.List(); err == nil {
		t.Fatal("want error when a source fails to list")
	}
}

func TestVersionListing(t *testing.T) {
	c := testCatalog()
	vs, err := c.Versions("mysql")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if strings.Join(vs, ",") != "1.0,1.1,2.0" {
		t.Fatalf("want 1.0,1.1,2.0; got %v", vs)
	}
}

func TestGetByIDAndVersion(t *testing.T) {
	c := testCatalog()
	e, err := c.Get("mysql", "1.1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.Version != "1.1" || e.PluginAPI != "opscore.plugin/v1" || e.MinKernel != "0.2.0" {
		t.Fatalf("bad metadata projection: %+v", e)
	}
	if len(e.Operations) != 1 || e.Operations[0].Risk != "low" {
		t.Fatalf("operations not projected: %+v", e.Operations)
	}
	if _, err := c.Get("nope", ""); err == nil {
		t.Fatal("want error for unknown plugin")
	}
}

func TestSearchByNamespaceKeywordSource(t *testing.T) {
	c := testCatalog()

	got, err := c.Search(Query{Namespace: "system."})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "systemd" {
		t.Fatalf("namespace search failed: %+v", got)
	}

	got, _ = c.Search(Query{Keyword: "dump"})
	if len(got) != 1 || got[0].Version != "2.0" {
		t.Fatalf("keyword search failed: %+v", got)
	}

	got, _ = c.Search(Query{Source: "oci"})
	if len(got) != 1 || got[0].Source != "oci" {
		t.Fatalf("source filter failed: %+v", got)
	}

	got, _ = c.Search(Query{})
	if len(got) != 4 {
		t.Fatalf("empty query should match all, got %d", len(got))
	}
}

// TestCatalogDoesNotDependOnRuntime enforces the GPT Round 26 architectural
// invariant: Catalog -> Provider -> PluginMetadata, and NEVER Catalog ->
// Manager. The catalog must not know the Runtime exists.
func TestCatalogDoesNotDependOnRuntime(t *testing.T) {
	forbidden := []string{
		"internal/plugin/runtime",
		"internal/core",
		"internal/builtin",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		af, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.Contains(p, bad) {
					t.Errorf("%s imports %q — catalog must not depend on the Runtime", f, p)
				}
			}
		}
	}
}
