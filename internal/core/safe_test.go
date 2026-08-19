package core

import "testing"

func TestSafeToken(t *testing.T) {
	ok := []string{"nginx", "vim", "user01", "1000", "/bin/bash", "my-pkg", "libc6:amd64"}
	for _, s := range ok {
		if err := SafeToken(s); err != nil {
			t.Fatalf("SafeToken(%q) unexpected error: %v", s, err)
		}
	}
	bad := []string{
		"",   // empty
		"-o", // leading dash -> flag
		"--allow-unauthenticated",
		"-u0",
		"foo bar", // whitespace
		"a;b",     // metacharacter
		"a|b",
		"a$(id)",
		"a`whoami`",
		"a>b",
	}
	for _, s := range bad {
		if err := SafeToken(s); err == nil {
			t.Fatalf("SafeToken(%q) should reject but passed", s)
		}
	}
}
