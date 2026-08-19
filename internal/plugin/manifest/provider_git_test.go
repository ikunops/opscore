package manifest_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// repoURL returns a git-cloneable local path. Git accepts a plain local
// filesystem path (forward slashes) directly; using file:// on Windows mangles
// the drive letter into an invalid /C:/... path, so we pass the path as-is.
func repoURL(path string) string {
	return filepath.ToSlash(path)
}

func TestGitProvider_SignedReadOK(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	priv, v := testKeypair(t)
	repo := t.TempDir()
	mustGit(t, repo, "init", "-q")

	data := []byte(demoManifest)
	kdir := filepath.Join(repo, "mysql")
	os.MkdirAll(kdir, 0o755)
	mp := filepath.Join(kdir, "manifest.json")
	os.WriteFile(mp, data, 0o644)
	sig, _ := manifest.Sign(priv, data)
	os.WriteFile(mp+".sig", sig, 0o644)
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-qm", "init")

	p := manifest.NewSignedGitProvider(repoURL(repo), "HEAD", "", v)
	defer func() {
		if c, ok := p.(interface{ Close() error }); ok {
			c.Close()
		}
	}()

	m, err := p.Read("mysql")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.Name != "demo" {
		t.Fatalf("name = %q, want demo", m.Name)
	}
	keys, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != "mysql" {
		t.Fatalf("List = %v, want [mysql]", keys)
	}
}

func TestGitProvider_TamperedFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	priv, v := testKeypair(t)
	repo := t.TempDir()
	mustGit(t, repo, "init", "-q")

	data := []byte(demoManifest)
	kdir := filepath.Join(repo, "mysql")
	os.MkdirAll(kdir, 0o755)
	mp := filepath.Join(kdir, "manifest.json")
	os.WriteFile(mp, data, 0o644)
	sig, _ := manifest.Sign(priv, data)
	os.WriteFile(mp+".sig", sig, 0o644)
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-qm", "init")

	// Tamper the manifest content but keep the OLD signature, then recommit.
	os.WriteFile(mp, []byte(`{"name":"demo","version":"1.0.0","operations":[{"name":"plugin.demo.x.z","resource":"x","action":"z"}]}`), 0o644)
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-qm", "tamper")

	p := manifest.NewSignedGitProvider(repoURL(repo), "HEAD", "", v)
	defer func() {
		if c, ok := p.(interface{ Close() error }); ok {
			c.Close()
		}
	}()
	if _, err := p.Read("mysql"); !errors.Is(err, manifest.ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for tampered manifest, got %v", err)
	}
}

func TestGitProvider_UnsignedOK(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	mustGit(t, repo, "init", "-q")
	kdir := filepath.Join(repo, "redis")
	os.MkdirAll(kdir, 0o755)
	os.WriteFile(filepath.Join(kdir, "manifest.json"), []byte(demoManifest), 0o644)
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-qm", "init")

	p := manifest.NewGitProvider(repoURL(repo), "HEAD", "")
	defer func() {
		if c, ok := p.(interface{ Close() error }); ok {
			c.Close()
		}
	}()
	if _, err := p.Read("redis"); err != nil {
		t.Fatalf("unsigned git read should succeed: %v", err)
	}
}
