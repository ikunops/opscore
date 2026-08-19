package manifest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/YuDong999/opscore/internal/plugin/manifest"
)

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type ociLayerSpec struct {
	title   string
	content []byte
}

// buildOCIImage builds an OCI image manifest JSON + a digest→blob map following
// the Phase 5.2 convention: a layer's annotation title names the file it holds
// (`<key>/manifest.json`, `<key>/manifest.json.sig`).
func buildOCIImage(t *testing.T, layers []ociLayerSpec) (string, map[string][]byte) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[`)
	blobs := map[string][]byte{}
	for i, l := range layers {
		d := sha256hex(l.content)
		blobs[d] = l.content
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"mediaType":"application/octet-stream","digest":"` + d +
			`","size":` + strconv.Itoa(len(l.content)) +
			`,"annotations":{"org.opencontainers.image.title":"` + l.title + `"}}`)
	}
	sb.WriteString(`]}`)
	return sb.String(), blobs
}

// newOCITestServer spins up a minimal OCI Distribution v2 registry (anonymous
// or bearer-protected) backed by in-memory blobs.
func newOCITestServer(t *testing.T, requireAuth bool, manifestJSON string, blobs map[string][]byte) *httptest.Server {
	t.Helper()
	const repo = "opscore/plugins"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		authOK := func() bool {
			if !requireAuth {
				return true
			}
			return r.Header.Get("Authorization") == "Bearer test-token"
		}
		switch {
		case strings.HasPrefix(p, "/v2/"+repo+"/manifests/"):
			if !authOK() {
				w.Header().Set("Www-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry",scope="repository:`+repo+`:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Write([]byte(manifestJSON))
		case strings.HasPrefix(p, "/v2/"+repo+"/blobs/"):
			if !authOK() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			digest := p[strings.LastIndex(p, "/")+1:]
			b, ok := blobs[digest]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(b)
		case p == "/token":
			w.Write([]byte(`{"token":"test-token"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOCIProvider_SignedReadOK(t *testing.T) {
	priv, v := testKeypair(t)
	manifestContent := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, manifestContent)
	mj, blobs := buildOCIImage(t, []ociLayerSpec{
		{title: "mysql/manifest.json", content: manifestContent},
		{title: "mysql/manifest.json.sig", content: sig},
	})
	srv := newOCITestServer(t, false, mj, blobs)

	p := manifest.NewSignedOCIProvider(srv.URL, "opscore/plugins", "latest", v)
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

func TestOCIProvider_TamperedFails(t *testing.T) {
	priv, v := testKeypair(t)
	manifestContent := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, manifestContent)
	// The blob served for the manifest is tampered; the signature is original,
	// so verification MUST fail.
	tampered := []byte(`{"name":"demo","version":"1.0.0","operations":[{"name":"plugin.demo.x.z","resource":"x","action":"z"}]}`)
	mj, blobs := buildOCIImage(t, []ociLayerSpec{
		{title: "mysql/manifest.json", content: tampered},
		{title: "mysql/manifest.json.sig", content: sig},
	})
	srv := newOCITestServer(t, false, mj, blobs)

	p := manifest.NewSignedOCIProvider(srv.URL, "opscore/plugins", "latest", v)
	if _, err := p.Read("mysql"); !errors.Is(err, manifest.ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for tampered manifest, got %v", err)
	}
}

func TestOCIProvider_UnsignedOK(t *testing.T) {
	manifestContent := []byte(demoManifest)
	mj, blobs := buildOCIImage(t, []ociLayerSpec{
		{title: "redis/manifest.json", content: manifestContent},
	})
	srv := newOCITestServer(t, false, mj, blobs)

	p := manifest.NewOCIProvider(srv.URL, "opscore/plugins", "latest")
	if _, err := p.Read("redis"); err != nil {
		t.Fatalf("unsigned oci read should succeed: %v", err)
	}
}

func TestOCIProvider_BearerAuth(t *testing.T) {
	priv, v := testKeypair(t)
	manifestContent := []byte(demoManifest)
	sig, _ := manifest.Sign(priv, manifestContent)
	mj, blobs := buildOCIImage(t, []ociLayerSpec{
		{title: "mysql/manifest.json", content: manifestContent},
		{title: "mysql/manifest.json.sig", content: sig},
	})
	srv := newOCITestServer(t, true, mj, blobs)

	p := manifest.NewSignedOCIProvider(srv.URL, "opscore/plugins", "latest", v)
	m, err := p.Read("mysql")
	if err != nil {
		t.Fatalf("Read with bearer auth: %v", err)
	}
	if m.Name != "demo" {
		t.Fatalf("name = %q, want demo", m.Name)
	}
}
