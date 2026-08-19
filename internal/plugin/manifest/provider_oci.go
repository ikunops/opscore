package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ociLayer is one entry in an OCI image manifest.
type ociLayer struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ociImageManifest is the subset of the OCI image manifest we consume.
// Convention: each plugin is a "directory" inside the image; a layer's
// `org.opencontainers.image.title` annotation names the file it holds
// (`<key>/manifest.json` and `<key>/manifest.json.sig`).
type ociImageManifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	MediaType     string     `json:"mediaType"`
	Config        ociLayer   `json:"config"`
	Layers        []ociLayer `json:"layers"`
}

// ociProvider reads manifests from an OCI registry. It is a PURELY PERIPHERAL
// Provider (GPT Round 22: Phase 5.2) — it implements the frozen `Provider`
// interface and reuses the Phase 5.1 Verifier, so the Loader and Runtime
// Contract stay untouched. It uses ONLY the standard library (net/http) against
// the OCI Distribution v2 API, so it adds ZERO third-party dependencies and
// compiles offline. The intent is to prove the Provider abstraction is
// source-agnostic: a manifest pulled over the wire flows through the SAME
// Verify → Parse → Validate path as one read from disk.
type ociProvider struct {
	registry   string // host[:port], e.g. "registry.example.com"
	repository string // image repository, e.g. "opscore/plugins"
	ref        string // tag or digest, e.g. "latest" / "sha256:…"
	client     *http.Client
	username   string
	password   string
	verifier   Verifier
	sigExt     string

	mu     sync.Mutex
	cached *ociImageManifest
	token  string // cached bearer token after a 401 challenge
}

// NewOCIProvider builds a Provider backed by an OCI registry. No signature
// verification is performed (use NewSignedOCIProvider to require it). No network
// access happens at construction — the manifest is pulled lazily on first
// List/Read.
func NewOCIProvider(registry, repository, ref string) Provider {
	return &ociProvider{registry: registry, repository: repository, ref: ref, client: http.DefaultClient, sigExt: ".sig"}
}

// NewSignedOCIProvider builds an OCIProvider that REQUIRES a valid detached
// signature for every manifest (fail-closed), reusing the Phase 5.1 Verifier.
func NewSignedOCIProvider(registry, repository, ref string, v Verifier) Provider {
	return &ociProvider{registry: registry, repository: repository, ref: ref, client: http.DefaultClient, verifier: v, sigExt: ".sig"}
}

// base returns the registry base URL. If registry already carries a scheme
// (e.g. "http://127.0.0.1:5000" for a local/test registry), it is used as-is;
// otherwise https:// is assumed for production registries.
func (p *ociProvider) base() string {
	if strings.Contains(p.registry, "://") {
		return p.registry
	}
	return "https://" + p.registry
}

func (p *ociProvider) manifestURL() string {
	return fmt.Sprintf("%s/v2/%s/manifests/%s", p.base(), p.repository, p.ref)
}

func (p *ociProvider) blobURL(digest string) string {
	return fmt.Sprintf("%s/v2/%s/blobs/%s", p.base(), p.repository, digest)
}

// doReq issues req, transparently performing the OCI bearer-token dance on a
// 401 (WWW-Authenticate: Bearer realm="…",service="…",scope="…"). The resolved
// token is cached for subsequent calls.
func (p *ociProvider) doReq(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	tok := p.token
	p.mu.Unlock()
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		realm, service, scope := parseWwwAuthenticate(resp.Header.Get("Www-Authenticate"))
		resp.Body.Close()
		if realm == "" {
			return nil, fmt.Errorf("oci provider: 401 without Bearer challenge")
		}
		ntok, terr := p.fetchToken(realm, service, scope)
		if terr != nil {
			return nil, terr
		}
		p.mu.Lock()
		p.token = ntok
		p.mu.Unlock()
		req2, _ := http.NewRequest(req.Method, req.URL.String(), nil)
		req2.Header.Set("Authorization", "Bearer "+ntok)
		req2.Header.Set("Accept", req.Header.Get("Accept"))
		return p.client.Do(req2)
	}
	return resp, nil
}

func (p *ociProvider) fetchToken(realm, service, scope string) (string, error) {
	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("oci provider: parse token realm: %w", err)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	req, _ := http.NewRequest("GET", u.String(), nil)
	if p.username != "" {
		req.SetBasicAuth(p.username, p.password)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oci provider: token endpoint status %d", resp.StatusCode)
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("oci provider: decode token: %w", err)
	}
	if tr.Token != "" {
		return tr.Token, nil
	}
	return tr.AccessToken, nil
}

func parseWwwAuthenticate(h string) (realm, service, scope string) {
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return "", "", ""
	}
	for _, part := range strings.Split(h[len("Bearer "):], ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := kv[0]
		v := strings.Trim(kv[1], `"`)
		switch k {
		case "realm":
			realm = v
		case "service":
			service = v
		case "scope":
			scope = v
		}
	}
	return realm, service, scope
}

func (p *ociProvider) getManifest() (*ociImageManifest, error) {
	p.mu.Lock()
	if p.cached != nil {
		c := p.cached
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	req, _ := http.NewRequest("GET", p.manifestURL(), nil)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	resp, err := p.doReq(req)
	if err != nil {
		return nil, fmt.Errorf("oci provider: get manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oci provider: get manifest: status %d", resp.StatusCode)
	}
	var m ociImageManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("oci provider: decode manifest: %w", err)
	}
	p.mu.Lock()
	p.cached = &m
	p.mu.Unlock()
	return &m, nil
}

func (p *ociProvider) getBlob(digest string) ([]byte, error) {
	req, _ := http.NewRequest("GET", p.blobURL(digest), nil)
	resp, err := p.doReq(req)
	if err != nil {
		return nil, fmt.Errorf("oci provider: get blob %s: %w", digest, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oci provider: get blob %s: status %d", digest, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, MaxManifestSize+MaxSignatureSize+1024))
}

// layerTitle returns the annotation title of a layer (our file-name convention).
func layerTitle(l ociLayer) string {
	if l.Annotations == nil {
		return ""
	}
	return l.Annotations["org.opencontainers.image.title"]
}

// List returns plugin keys found among the image layers (each <key>/manifest.json).
func (p *ociProvider) List() ([]string, error) {
	m, err := p.getManifest()
	if err != nil {
		return nil, err
	}
	keys := map[string]struct{}{}
	for _, l := range m.Layers {
		title := layerTitle(l)
		if !strings.HasSuffix(title, "/manifest.json") {
			continue
		}
		key := strings.TrimSuffix(title, "/manifest.json")
		if key == "" || strings.Contains(key, "/") {
			continue
		}
		keys[key] = struct{}{}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// Read loads, verifies and validates <key>/manifest.json from the image layers.
func (p *ociProvider) Read(key string) (*Manifest, error) {
	if key == "" {
		return nil, fmt.Errorf("oci provider: empty key")
	}
	if filepath.Base(key) != key || key == "." || key == ".." {
		return nil, fmt.Errorf("oci provider: invalid key %q", key)
	}
	m, err := p.getManifest()
	if err != nil {
		return nil, err
	}
	want := key + "/manifest.json"
	sigWant := key + "/manifest.json" + p.sigExt
	var data, sig []byte
	found := false
	for _, l := range m.Layers {
		switch layerTitle(l) {
		case want:
			data, err = p.getBlob(l.Digest)
			if err != nil {
				return nil, fmt.Errorf("oci provider: read %q: %w", key, err)
			}
			found = true
		case sigWant:
			if p.verifier != nil {
				sig, err = p.getBlob(l.Digest)
				if err != nil {
					return nil, fmt.Errorf("oci provider: read sig %q: %w", key, err)
				}
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("oci provider: %q: manifest layer not found", key)
	}
	if len(data) > MaxManifestSize {
		return nil, fmt.Errorf("oci provider: %q manifest too large (%d > %d bytes)", key, len(data), MaxManifestSize)
	}
	// Phase 5.3 peripheral trust gate: verify the detached signature and
	// enforce the signature policy BEFORE Parse.
	if p.verifier != nil {
		if len(sig) == 0 {
			return nil, fmt.Errorf("oci provider: %q: %w", key, ErrSignatureMissing)
		}
		if _, verr := VerifyManifest(key, data, sig, p.verifier); verr != nil {
			return nil, fmt.Errorf("oci provider: %q: %w", key, verr)
		}
	}
	mm, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := mm.Validate(); err != nil {
		return nil, fmt.Errorf("oci provider: validate %q: %w", key, err)
	}
	if err := mm.ValidateManifestLimits(); err != nil {
		return nil, fmt.Errorf("oci provider: limits %q: %w", key, err)
	}
	return mm, nil
}
