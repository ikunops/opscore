package manifest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// gitProvider reads manifests from a git repository reachable by the local
// `git` CLI (https://, ssh://, file://, …). It is a PURELY PERIPHERAL Provider
// (GPT Round 22: Phase 5.2) — it implements the frozen `Provider` interface and
// reuses the Phase 5.1 Verifier, so the Loader and the Runtime Contract are
// untouched. The goal is to prove the Provider abstraction is genuinely
// source-agnostic: a manifest pulled from git flows through the SAME
// Verify → Parse → Validate path as one read from disk.
//
// Inside the repo (under Root) the expected layout mirrors FileProvider:
//
//	<Root>/<key>/manifest.json
//	<Root>/<key>/manifest.json.sig   (present iff a verifier is configured)
type gitProvider struct {
	repoURL  string // any URL `git clone` accepts
	ref      string // branch / tag / commit the provider reads from
	root     string // manifest directory inside the repo ("" or "." == repo root)
	gitBin   string // git executable (default "git")
	verifier Verifier
	sigExt   string

	cacheDir string
	once     sync.Once
	cloneErr error
}

// NewGitProvider builds a Provider backed by a git repository. No signature
// verification is performed (use NewSignedGitProvider to require it). No disk
// access happens at construction — the clone is lazy on first List/Read.
func NewGitProvider(repoURL, ref, root string) Provider {
	return &gitProvider{repoURL: repoURL, ref: ref, root: root, gitBin: "git"}
}

// NewSignedGitProvider builds a GitProvider that REQUIRES a valid detached
// signature for every manifest (fail-closed), reusing the Phase 5.1 Verifier.
// It does not alter the Provider contract or the Runtime Contract.
func NewSignedGitProvider(repoURL, ref, root string, v Verifier) Provider {
	return &gitProvider{repoURL: repoURL, ref: ref, root: root, gitBin: "git", verifier: v, sigExt: ".sig"}
}

// Close releases the local clone. It is NOT part of the Provider interface
// (Providers are read-only sources); call it when discarding the provider.
func (p *gitProvider) Close() error {
	if p.cacheDir != "" {
		return os.RemoveAll(p.cacheDir)
	}
	return nil
}

func (p *gitProvider) ensureClone() error {
	p.once.Do(func() {
		dir, err := os.MkdirTemp("", "opscore-git-provider-*")
		if err != nil {
			p.cloneErr = fmt.Errorf("git provider: mkdtemp: %w", err)
			return
		}
		p.cacheDir = dir
		cmd := exec.Command(p.gitBin, "clone", "--no-checkout", p.repoURL, dir)
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			p.cloneErr = fmt.Errorf("git provider: clone %q: %w: %s", p.repoURL, cerr, out)
		}
	})
	return p.cloneErr
}

// manifestPath returns the in-repo path of a plugin's manifest.json.
func (p *gitProvider) manifestPath(key string) string {
	if p.root == "" || p.root == "." {
		return key + "/manifest.json"
	}
	return p.root + "/" + key + "/manifest.json"
}

// show reads a blob at <ref>:<path> via `git show` (no worktree checkout).
func (p *gitProvider) show(path string) ([]byte, error) {
	return exec.Command(p.gitBin, "-C", p.cacheDir, "show", p.ref+":"+path).Output()
}

// List returns plugin keys present under Root, derived from each
// <key>/manifest.json found via `git ls-tree -r`.
func (p *gitProvider) List() ([]string, error) {
	if err := p.ensureClone(); err != nil {
		return nil, err
	}
	args := []string{"-C", p.cacheDir, "ls-tree", "-r", "-z", p.ref}
	if p.root != "" && p.root != "." {
		args = append(args, "--", p.root)
	}
	out, err := exec.Command(p.gitBin, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("git provider: ls-tree %q: %w", p.ref, err)
	}
	keys := map[string]struct{}{}
	for _, line := range strings.Split(string(out), "\x00") {
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		path := line[tab+1:]
		if !strings.HasSuffix(path, "/manifest.json") {
			continue
		}
		rel := strings.TrimSuffix(path, "/manifest.json")
		key := rel
		if p.root != "" && p.root != "." {
			key = strings.TrimPrefix(rel, p.root+"/")
		}
		if key == "" || strings.Contains(key, "/") {
			continue // only top-level keys, ignore nested
		}
		keys[key] = struct{}{}
	}
	out2 := make([]string, 0, len(keys))
	for k := range keys {
		out2 = append(out2, k)
	}
	sort.Strings(out2) // deterministic order (Round 10 SHOULD)
	return out2, nil
}

// Read loads, verifies and validates <Root>/<key>/manifest.json from the repo.
func (p *gitProvider) Read(key string) (*Manifest, error) {
	if key == "" {
		return nil, fmt.Errorf("git provider: empty key")
	}
	if filepath.Base(key) != key || key == "." || key == ".." {
		return nil, fmt.Errorf("git provider: invalid key %q", key)
	}
	if err := p.ensureClone(); err != nil {
		return nil, err
	}
	data, err := p.show(p.manifestPath(key))
	if err != nil {
		return nil, fmt.Errorf("git provider: read %q: %w", key, err)
	}
	if len(data) > MaxManifestSize {
		return nil, fmt.Errorf("git provider: %q manifest too large (%d > %d bytes)", key, len(data), MaxManifestSize)
	}
	// Phase 5.3 peripheral trust gate: verify the detached signature and
	// enforce the signature policy BEFORE Parse. The sig is read from the same
	// ref, same path, with the .sig extension.
	if p.verifier != nil {
		sig, serr := p.show(p.manifestPath(key) + p.sigExt)
		if serr != nil {
			return nil, fmt.Errorf("git provider: %q: %w", key, ErrSignatureMissing)
		}
		if _, verr := VerifyManifest(key, data, sig, p.verifier); verr != nil {
			return nil, fmt.Errorf("git provider: %q: %w", key, verr)
		}
	}
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("git provider: validate %q: %w", key, err)
	}
	if err := m.ValidateManifestLimits(); err != nil {
		return nil, fmt.Errorf("git provider: limits %q: %w", key, err)
	}
	return m, nil
}
