package manifest

import (
	"strings"
	"testing"
)

func TestParse_LegacySchema0(t *testing.T) {
	data := []byte(`{"name":"mysql","version":"1.0.0","operations":[{"name":"plugin.mysql.db.query","resource":"db","action":"query"}]}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "mysql" {
		t.Fatalf("Name = %q, want mysql", m.Name)
	}
	if m.SchemaVersion != 0 {
		t.Fatalf("SchemaVersion = %d, want 0 (legacy/unversioned)", m.SchemaVersion)
	}
}

func TestParse_SchemaV1(t *testing.T) {
	data := []byte(`{"schemaVersion":1,"name":"redis","version":"0.9.0","operations":[{"name":"plugin.redis.cache.get","resource":"cache","action":"get"}]}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "redis" || m.SchemaVersion != 1 {
		t.Fatalf("unexpected manifest: name=%q schema=%d", m.Name, m.SchemaVersion)
	}
}

func TestParse_UnsupportedSchemaRejected(t *testing.T) {
	data := []byte(`{"schemaVersion":999,"name":"x","version":"1.0.0","operations":[]}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected rejection of unsupported schema version 999")
	} else if !strings.Contains(err.Error(), "unsupported schema version") {
		t.Fatalf("error should mention unsupported schema version, got %v", err)
	}
}

func TestSupportedSchemaVersions(t *testing.T) {
	got := SupportedSchemaVersions()
	want := []int{0, 1}
	if len(got) != len(want) {
		t.Fatalf("SupportedSchemaVersions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SupportedSchemaVersions = %v, want %v", got, want)
		}
	}
}

// TestRegisterParser allows a NEW schema version to be wired in without
// touching Parse — the core of Phase 3.8's evolvability. Uses
// MustRegisterParser (the kernel-init registration API, GPT Round 14 SHOULD).
func TestRegisterParser(t *testing.T) {
	called := false
	MustRegisterParser(2, func(data []byte) (*Manifest, error) {
		called = true
		return parseV0(data)
	})
	defer func() { delete(parsers, 2) }() // keep registry clean for other tests

	data := []byte(`{"schemaVersion":2,"name":"future","version":"1.0.0","operations":[]}`)
	if _, err := Parse(data); err != nil {
		t.Fatalf("schema 2 should parse via registered parser: %v", err)
	}
	if !called {
		t.Fatal("registered schema-2 parser was not invoked")
	}
}

func TestParse_MalformedJSON(t *testing.T) {
	if _, err := Parse([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
