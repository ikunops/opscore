package core

import "testing"

func TestBuildRemoteCommand_Quoting(t *testing.T) {
	got := buildRemoteCommand(TargetHost{}, "systemctl", []string{"restart", "nginx.service"})
	want := "systemctl 'restart' 'nginx.service'"
	if got != want {
		t.Fatalf("plain: got %q want %q", got, want)
	}
}

func TestBuildRemoteCommand_InjectionSafe(t *testing.T) {
	// An argument containing shell metacharacters must be wrapped, not split.
	got := buildRemoteCommand(TargetHost{}, "systemctl", []string{"restart", "a;b && rm -rf /"})
	want := "systemctl 'restart' 'a;b && rm -rf /'"
	if got != want {
		t.Fatalf("injection: got %q want %q", got, want)
	}
}

func TestBuildRemoteCommand_Sudo(t *testing.T) {
	got := buildRemoteCommand(TargetHost{Sudo: true}, "systemctl", []string{"restart", "nginx.service"})
	want := "sudo -n systemctl 'restart' 'nginx.service'"
	if got != want {
		t.Fatalf("sudo: got %q want %q", got, want)
	}
	// Without Sudo, no prefix.
	plain := buildRemoteCommand(TargetHost{}, "systemctl", []string{"restart", "nginx.service"})
	if plain != "systemctl 'restart' 'nginx.service'" {
		t.Fatalf("no-sudo regression: %q", plain)
	}
}
