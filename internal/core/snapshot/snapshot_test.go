package snapshot

import (
	"testing"
)

func sample(overrides map[string]bool) *CapabilitySnapshot {
	base := map[string]bool{
		"systemd":    true,
		"service":    false,
		"firewalld":  true,
		"ufw":        false,
		"iptables":   true,
		"docker":     false,
		"journalctl": true,
	}
	for k, v := range overrides {
		base[k] = v
	}
	items := map[string]CapabilityInfo{}
	for k, v := range base {
		items[k] = CapabilityInfo{Name: k, Available: v}
	}
	return &CapabilitySnapshot{HostID: "h1", Items: items, Source: SourceLocal}
}

func TestHash_StableAndSensitive(t *testing.T) {
	a := sample(nil)
	b := sample(nil)
	if a.Hash() != b.Hash() {
		t.Fatalf("hash not stable for identical snapshots: %q vs %q", a.Hash(), b.Hash())
	}
	c := sample(map[string]bool{"systemd": false})
	if a.Hash() == c.Hash() {
		t.Fatalf("hash should change when a capability flips")
	}
	// Different version string must change the hash too.
	items := map[string]CapabilityInfo{"systemd": {Name: "systemd", Available: true, Version: "255"}}
	d := &CapabilitySnapshot{HostID: "h1", Items: items, Source: SourceLocal}
	items2 := map[string]CapabilityInfo{"systemd": {Name: "systemd", Available: true, Version: "256"}}
	e := &CapabilitySnapshot{HostID: "h1", Items: items2, Source: SourceLocal}
	if d.Hash() == e.Hash() {
		t.Fatalf("hash should change when version differs")
	}
}

func TestAvailableAndGet(t *testing.T) {
	s := sample(nil)
	if !s.Available("systemd") {
		t.Fatal("systemd should be available")
	}
	if s.Available("docker") {
		t.Fatal("docker should be unavailable")
	}
	info, ok := s.Get("firewalld")
	if !ok || !info.Available {
		t.Fatalf("Get(firewalld) = %+v ok=%v", info, ok)
	}
	_, ok = s.Get("nonexistent")
	if ok {
		t.Fatal("Get on missing key must report not-ok")
	}
}

func TestSortedDeterministic(t *testing.T) {
	s := sample(nil)
	got := s.Sorted()
	if len(got) != len(s.Items) {
		t.Fatalf("Sorted length %d != %d", len(got), len(s.Items))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Fatalf("Sorted not ascending: %q then %q", got[i-1].Name, got[i].Name)
		}
	}
}

func TestNilSafe(t *testing.T) {
	var s *CapabilitySnapshot
	if s.Available("x") {
		t.Fatal("nil snapshot must report not-available")
	}
	if s.Hash() != "" {
		t.Fatal("nil snapshot hash must be empty")
	}
	if s.Sorted() != nil {
		t.Fatal("nil snapshot Sorted must be nil")
	}
}
