package registry

import (
	"context"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestRegistryImportsOnlyStdlib pins the Phase 7.4 boundary: this package must
// not import any internal/ package. It is the same AST guard pattern used in
// ecosystem/sdk, ecosystem/packaging, and ecosystem/tooling.
func TestRegistryImportsOnlyStdlib(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(path, "internal/") || strings.Contains(path, "/internal/") {
					t.Errorf("registry must not import internal/ packages, found %s", path)
				}
			}
		}
	}
}

func sampleIndex() *Index {
	return &Index{
		Registry: RegistryMeta{ID: "opscore", Name: "OpsCore Community Registry"},
		Packages: []PackageRef{
			{
				ID:                "opscore/mysql-helper",
				Name:              "MySQL Helper",
				Description:       "Backup and query MySQL",
				LatestVersion:     "1.4.0",
				AvailableVersions: []string{"1.3.0", "1.4.0"},
				SDKVersion:        "opscore.isolation/v1",
				Tags:              []string{"database", "backup"},
				DownloadURL:       "https://registry.example.com/opscore/mysql-helper/1.4.0.tar.gz",
				Checksums:         map[string]string{"mysql-helper": "abc123"},
				SignatureRef:      "sig:opscore/mysql-helper@1.4.0",
			},
			{
				ID:                "opscore/backup-helper",
				Name:              "Backup Helper",
				Description:       "Generic file backup",
				LatestVersion:     "2.1.0",
				AvailableVersions: []string{"2.0.0", "2.1.0"},
				SDKVersion:        "opscore.isolation/v1",
				Tags:              []string{"backup"},
				DownloadURL:       "https://registry.example.com/opscore/backup-helper/2.1.0.tar.gz",
			},
		},
	}
}

func TestIndexRoundTrip(t *testing.T) {
	idx := sampleIndex()
	data, err := idx.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("want 2 packages, got %d", len(got.Packages))
	}
	if got.Packages[0].ID != "opscore/mysql-helper" {
		t.Errorf("unexpected first package: %s", got.Packages[0].ID)
	}
	// round-trip must preserve checksums
	if got.Packages[0].Checksums["mysql-helper"] != "abc123" {
		t.Errorf("checksum not preserved across round-trip")
	}
}

func TestParseIndexRejectsInvalidPackage(t *testing.T) {
	bad := []byte(`{"registry":{"id":"x"},"packages":[{"id":"","name":"","latestVersion":"","sdkVersion":"","downloadURL":""}]}`)
	if _, err := ParseIndex(bad); err == nil {
		t.Fatal("expected validation error for empty PackageRef")
	}
}

func TestMemoryRegistrySearchAndGet(t *testing.T) {
	reg := FromIndex(sampleIndex())

	// Search by tag (AND filter)
	res, err := reg.Search(context.Background(), SearchQuery{Tags: []string{"database"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Items[0].ID != "opscore/mysql-helper" {
		t.Fatalf("tag search wrong: total=%d items=%v", res.Total, res.Items)
	}

	// Search by term (case-insensitive, matches name/description/id)
	res, err = reg.Search(context.Background(), SearchQuery{Term: "BACKUP"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("term search want 2, got %d", res.Total)
	}

	// Windowing is honest: Total reports true count, Items is the window
	res, err = reg.Search(context.Background(), SearchQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 || len(res.Items) != 1 {
		t.Fatalf("windowing wrong: total=%d items=%d", res.Total, len(res.Items))
	}

	// Get existing
	p, err := reg.Get(context.Background(), "opscore/backup-helper")
	if err != nil {
		t.Fatal(err)
	}
	if p.LatestVersion != "2.1.0" {
		t.Errorf("unexpected version %s", p.LatestVersion)
	}

	// Get missing
	if _, err := reg.Get(context.Background(), "nope"); err == nil {
		t.Fatal("expected not-found error")
	}

	// Versions
	vs, err := reg.Versions(context.Background(), "opscore/mysql-helper")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("want 2 versions, got %d", len(vs))
	}

	// List returns all
	all, err := reg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("list want 2, got %d", len(all))
	}
}
