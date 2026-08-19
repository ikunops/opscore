package capability

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	c, err := Parse("os.linux")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if c.Name != "os.linux" {
		t.Errorf("Name = %q", c.Name)
	}
	if c.Namespace != "os" {
		t.Errorf("Namespace = %q, want os", c.Namespace)
	}
	if !c.Required {
		t.Errorf("Required default should be true")
	}
}

func TestParse_NoDot(t *testing.T) {
	c, err := Parse("linux")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if c.Namespace != "linux" {
		t.Errorf("Namespace = %q, want linux (no-dot => self-namespace)", c.Namespace)
	}
}

func TestParse_Empty(t *testing.T) {
	if _, err := Parse(""); err == nil {
		t.Fatal("expected error for empty capability name")
	}
}

func TestParseOptional(t *testing.T) {
	c, err := ParseOptional("fs.zfs")
	if err != nil {
		t.Fatalf("ParseOptional error: %v", err)
	}
	if c.Required {
		t.Errorf("ParseOptional should set Required=false")
	}
}

func TestHostProvider_Has(t *testing.T) {
	h := NewHostProvider([]string{"os.linux", "net.tcp"})
	if !h.Has("os.linux") {
		t.Error("expected host to provide os.linux")
	}
	if h.Has("fs.zfs") {
		t.Error("host should not provide fs.zfs")
	}
}

func TestNegotiate_AllGranted(t *testing.T) {
	required, _ := Parse("os.linux")
	required2, _ := Parse("net.tcp")
	res := Negotiate([]Capability{required, required2}, NewHostProvider([]string{"os.linux", "net.tcp"}))
	if !res.AllGranted {
		t.Errorf("AllGranted = false, want true; Missing=%v", res.Missing)
	}
	if len(res.Missing) != 0 {
		t.Errorf("Missing = %v, want empty", res.Missing)
	}
	if len(res.OptionalMissing) != 0 {
		t.Errorf("OptionalMissing = %v, want empty", res.OptionalMissing)
	}
}

func TestNegotiate_MissingRequired(t *testing.T) {
	req, _ := Parse("docker")
	res := Negotiate([]Capability{req}, NewHostProvider([]string{"linux"}))
	if res.AllGranted {
		t.Error("AllGranted = true, want false (required missing)")
	}
	if len(res.Missing) != 1 || res.Missing[0] != "docker" {
		t.Errorf("Missing = %v, want [docker]", res.Missing)
	}
}

func TestNegotiate_OptionalMissingDoesNotBlock(t *testing.T) {
	req, _ := Parse("os.linux")       // required, present
	opt, _ := ParseOptional("fs.zfs") // optional, absent
	res := Negotiate([]Capability{req, opt}, NewHostProvider([]string{"os.linux"}))
	if !res.AllGranted {
		t.Error("AllGranted should be true: optional-missing must not block")
	}
	if len(res.OptionalMissing) != 1 || res.OptionalMissing[0] != "fs.zfs" {
		t.Errorf("OptionalMissing = %v, want [fs.zfs]", res.OptionalMissing)
	}
	if len(res.Missing) != 0 {
		t.Errorf("Missing = %v, want empty", res.Missing)
	}
}

func TestNegotiate_Mixed(t *testing.T) {
	req, _ := Parse("os.linux")       // present
	req2, _ := Parse("docker")        // required, absent
	opt, _ := ParseOptional("fs.zfs") // optional, absent
	res := Negotiate([]Capability{req, req2, opt}, NewHostProvider([]string{"os.linux"}))
	if res.AllGranted {
		t.Error("AllGranted should be false (docker required, missing)")
	}
	if len(res.Missing) != 1 || !strings.Contains(res.Missing[0], "docker") {
		t.Errorf("Missing = %v, want [docker]", res.Missing)
	}
	if len(res.OptionalMissing) != 1 || res.OptionalMissing[0] != "fs.zfs" {
		t.Errorf("OptionalMissing = %v, want [fs.zfs]", res.OptionalMissing)
	}
}

func TestNegotiate_NilHost(t *testing.T) {
	req, _ := Parse("os.linux")
	res := Negotiate([]Capability{req}, nil)
	if res.AllGranted {
		t.Error("AllGranted should be false with nil host (nothing provided)")
	}
	if len(res.Missing) != 1 {
		t.Errorf("Missing = %v, want [os.linux]", res.Missing)
	}
}
