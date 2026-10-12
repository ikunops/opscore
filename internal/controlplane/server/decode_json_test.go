package server

// T399 (ADR-089/090, P55/P2-2): decodeJSON must reject an ABSENT body
// explicitly instead of silently proceeding with a zero value — the P2-2
// finding: the downstream 401 from a zero credentials{} was a confusing
// failure mode for what is plainly a bad request. Normal bodies and empty
// (non-absent) bodies keep their exact existing behaviour.

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestT399DecodeJSONAbsentBodyIsExplicitError(t *testing.T) {
	// Absent body (nil Body): explicit error, never a silent zero value.
	var dst struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(&http.Request{}, &dst); err == nil {
		t.Fatal("T399: an absent request body must be an explicit error, got nil")
	}

	// A normal body decodes exactly as before.
	req, err := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"alice"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeJSON(req, &dst); err != nil {
		t.Fatalf("T399: a normal body must decode, got %v", err)
	}
	if dst.Username != "alice" {
		t.Fatalf("T399: decoded username = %q, want alice", dst.Username)
	}

	// An EMPTY (non-absent) body stays the documented io.EOF path: the
	// body-required call sites map it to 400; decodeParams is the endpoint
	// family that deliberately tolerates absence.
	req2, err := http.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeJSON(req2, &dst); !errors.Is(err, io.EOF) {
		t.Fatalf("T399: an empty body must stay on the io.EOF path, got %v", err)
	}
}
