package main

import (
	"os"
	"testing"
)

func TestLoadConfigFile(t *testing.T) {
	path := ".cfgtest.env"
	content := "# comment\n\naddr=:9999\nstorage=sqlite\n# another\ntarget-insecure=true\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	vals, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if vals["addr"] != ":9999" {
		t.Errorf("addr = %q, want :9999", vals["addr"])
	}
	if vals["storage"] != "sqlite" {
		t.Errorf("storage = %q, want sqlite", vals["storage"])
	}
	if vals["target-insecure"] != "true" {
		t.Errorf("target-insecure = %q, want true", vals["target-insecure"])
	}
	if len(vals) != 3 {
		t.Errorf("expected 3 keys (comments/blanks ignored), got %d: %v", len(vals), vals)
	}
}

func TestLoadConfigFile_MissingIsOK(t *testing.T) {
	vals, err := loadConfigFile("/nonexistent/opscore.env")
	if err != nil {
		t.Fatalf("missing file should be a nil error, got %v", err)
	}
	if len(vals) != 0 {
		t.Errorf("expected empty map, got %v", vals)
	}
}

func TestCfgVal_Precedence(t *testing.T) {
	t.Run("file over env over default", func(t *testing.T) {
		t.Setenv("OPESCORE_ADDR", ":7777") // env present
		vals := map[string]string{"addr": ":8888"}
		if got := cfgVal(vals, "addr", ":8080"); got != ":8888" {
			t.Errorf("file should win over env/default: got %q", got)
		}
		delete(vals, "addr")
		if got := cfgVal(vals, "addr", ":8080"); got != ":7777" {
			t.Errorf("env should win over default: got %q", got)
		}
	})
	t.Run("default when neither", func(t *testing.T) {
		if got := cfgVal(map[string]string{}, "addr", ":8080"); got != ":8080" {
			t.Errorf("default should apply: got %q", got)
		}
	})
}

func TestCfgInt(t *testing.T) {
	if got := cfgInt(map[string]string{"target-port": "2222"}, "target-port", 22); got != 2222 {
		t.Errorf("file int: got %d", got)
	}
	if got := cfgInt(map[string]string{}, "target-port", 22); got != 22 {
		t.Errorf("default int: got %d", got)
	}
	if got := cfgInt(map[string]string{"target-port": "abc"}, "target-port", 22); got != 22 {
		t.Errorf("invalid int -> default: got %d", got)
	}
}

func TestCfgBool(t *testing.T) {
	if !cfgBool(map[string]string{"target-insecure": "yes"}, "target-insecure", false) {
		t.Error("yes -> true")
	}
	if cfgBool(map[string]string{"target-insecure": "off"}, "target-insecure", true) {
		t.Error("off -> false")
	}
	if !cfgBool(map[string]string{"demo": "1"}, "demo", false) {
		t.Error("1 -> true")
	}
	if cfgBool(map[string]string{}, "demo", false) {
		t.Error("default false")
	}
}

func TestValidateBootstrapAdminPassword(t *testing.T) {
	// gateApplies=false (demo or memory mode) => policy exempt, always ok.
	t.Run("exempt when gate not applicable", func(t *testing.T) {
		cases := []struct{ user, pass string }{
			{"admin", "admin"},   // weak but exempt
			{"root", "root"},     // username==pass but exempt
			{"", ""},             // empty but exempt
		}
		for _, c := range cases {
			if ok, _ := validateBootstrapAdminPassword(c.user, c.pass, false); !ok {
				t.Errorf("gateApplies=false must be exempt: user=%q pass=%q", c.user, c.pass)
			}
		}
	})

	t.Run("rejects weak credentials", func(t *testing.T) {
		cases := []struct {
			user, pass, wantReason string
		}{
			{"admin", "", "empty"},
			{"admin", "admin", "known-default"},
			{"admin", "Admin", "known-default"},
			{"admin", "ADMIN", "known-default"},
			{"admin", "change-me", "known-default"},
			{"admin", "Change-Me", "known-default"},
			{"admin", "CHANGE-ME", "known-default"},
			{"admin", "change-me-in-prod", "known-default"},
			{"admin", "Change-Me-In-Prod", "known-default"},
			{"root", "root", "username-equals"}, // not a known default
			{"Root", "ROOT", "username-equals"},
			{"admin", "shortpass11", "too-short"}, // 11 chars
		}
		for _, c := range cases {
			ok, reason := validateBootstrapAdminPassword(c.user, c.pass, true)
			if ok {
				t.Errorf("expected reject: user=%q pass=%q", c.user, c.pass)
			}
			if reason != c.wantReason {
				t.Errorf("reason = %q, want %q (user=%q pass=%q)", reason, c.wantReason, c.user, c.pass)
			}
		}
	})

	t.Run("accepts strong non-default password", func(t *testing.T) {
		ok, reason := validateBootstrapAdminPassword("admin", "a-very-strong-pass", true)
		if !ok {
			t.Errorf("expected accept, got reason=%q", reason)
		}
		// exactly 12 chars, non-default, not equal to username
		ok, reason = validateBootstrapAdminPassword("admin", "shortpass112", true)
		if !ok {
			t.Errorf("expected accept at 12 chars, got reason=%q", reason)
		}
	})
}
