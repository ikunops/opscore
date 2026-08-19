package oci

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/ecosystem/packaging"
)

// TestOCIImportsOnlyStdlib pins the Phase 7.6 boundary: this package must not
// import any internal/ package (the frozen Runtime Core). It may import sibling
// ecosystem packages (packaging / registry) — those are not internal/.
func TestOCIImportsOnlyStdlib(t *testing.T) {
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
					t.Errorf("oci must not import internal/ packages, found %s", path)
				}
			}
		}
	}
}

func mustDigest(t *testing.T, hex string) Digest {
	t.Helper()
	d, err := ParseDigest("sha256:" + hex)
	if err != nil {
		t.Fatalf("parse digest: %v", err)
	}
	return d
}

func TestParseDigestValidates(t *testing.T) {
	if _, err := ParseDigest("sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	for _, bad := range []string{
		"md5:abc",
		"sha256:xyz",
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("g", 64),
		"noseparator",
	} {
		if _, err := ParseDigest(bad); err == nil {
			t.Errorf("expected error for digest %q", bad)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		MediaType:     MediaTypePluginManifest,
		Config: Descriptor{
			MediaType: MediaTypePluginConfig,
			Digest:    mustDigest(t, strings.Repeat("c", 64)).String(),
			Size:      512,
		},
		Layers: []Descriptor{
			{
				MediaType:   MediaTypePluginLayer,
				Digest:      mustDigest(t, strings.Repeat("d", 64)).String(),
				Size:        2048,
				Annotations: map[string]string{"opscore.file": "bin/helper"},
			},
		},
	}
	b, err := MarshalManifest(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Config.Digest != m.Config.Digest || len(got.Layers) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestManifestValidateRejectsBadSchema(t *testing.T) {
	m := &Manifest{SchemaVersion: 1, MediaType: MediaTypePluginManifest}
	if _, err := MarshalManifest(m); err == nil {
		t.Fatal("expected schemaVersion rejection")
	}
}

func TestLayoutRoundTrip(t *testing.T) {
	b, err := MarshalLayout(NewLayout())
	if err != nil {
		t.Fatal(err)
	}
	l, err := ParseLayout(b)
	if err != nil {
		t.Fatal(err)
	}
	if l.ImageLayoutVersion != ImageLayoutVersion {
		t.Fatalf("version mismatch: %s", l.ImageLayoutVersion)
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in   string
		name string
		ver  string
		dig  string
	}{
		{"myplugin:1.2.3", "myplugin", "1.2.3", ""},
		{"myplugin@sha256:" + strings.Repeat("e", 64), "myplugin", "", "sha256:" + strings.Repeat("e", 64)},
		{"myplugin", "myplugin", "", ""},
	}
	for _, c := range cases {
		tag, err := ParseRef(c.in)
		if err != nil {
			t.Fatalf("parse %q: %v", c.in, err)
		}
		if tag.Name != c.name || tag.Version != c.ver || tag.Digest != c.dig {
			t.Errorf("parse %q = %+v, want name=%q ver=%q dig=%q", c.in, tag, c.name, c.ver, c.dig)
		}
	}
	if _, err := ParseRef(""); err == nil {
		t.Error("expected error for empty ref")
	}
}

func TestFromPackageOrdersPluginJSONFirst(t *testing.T) {
	pkg := &packaging.Package{Name: "demo", Version: "1.0.0"}
	files := map[string]FileHash{
		"bin/helper":   {Digest: mustDigest(t, strings.Repeat("1", 64)), Size: 1024},
		"plugin.json":  {Digest: mustDigest(t, strings.Repeat("2", 64)), Size: 256},
		"release.json": {Digest: mustDigest(t, strings.Repeat("3", 64)), Size: 128},
	}
	a := FromPackage(pkg, files)
	if a.Ref.Name != "demo" || a.Ref.Version != "1.0.0" {
		t.Fatalf("ref wrong: %+v", a.Ref)
	}
	if len(a.Blobs) != 3 {
		t.Fatalf("expected 3 blobs, got %d", len(a.Blobs))
	}
	if a.Blobs[0].MediaType != MediaTypePluginConfig || a.Blobs[0].Annotations["opscore.file"] != "plugin.json" {
		t.Fatalf("plugin.json must be first config blob, got %+v", a.Blobs[0])
	}
}

func TestToPackageRefReusesRegistryModel(t *testing.T) {
	a := Artifact{Ref: Tag{Name: "demo", Version: "1.0.0"}}
	meta := PackageMeta{
		SDKVersion:       "opscore.isolation/v1",
		SupportedRuntime: ">=1.0,<2.0",
		MinRuntime:       "1.0.0",
		MaxRuntime:       "1.9.0",
		Description:      "demo plugin",
		DownloadURL:      "oci://registry.example.com/demo:1.0.0",
		SignatureRef:     "trust://demo/1.0.0",
	}
	ref := a.ToPackageRef(meta)
	if ref.ID != "demo" || ref.Name != "demo" || ref.LatestVersion != "1.0.0" {
		t.Fatalf("ref identity wrong: %+v", ref)
	}
	if ref.SDKVersion != meta.SDKVersion || ref.MinRuntime != meta.MinRuntime || ref.MaxRuntime != meta.MaxRuntime {
		t.Fatalf("ref reused metadata lost: %+v", ref)
	}
	if ref.DownloadURL != meta.DownloadURL || ref.SignatureRef != meta.SignatureRef {
		t.Fatalf("ref transport/trust refs lost: %+v", ref)
	}
}
