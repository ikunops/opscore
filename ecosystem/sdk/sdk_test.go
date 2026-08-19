package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/parser"
	goToken "go/token"
	"os"
	"strings"
	"testing"
)

// TestSDKImportsOnlyStdlib pins MUST-4 / MUST-5: the SDK is the public,
// standalone protocol reference. It must not drag in core, runtime, or any
// internal package — a third-party plugin repo compiles against it alone.
func TestSDKImportsOnlyStdlib(t *testing.T) {
	fset := goToken.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read sdk dir: %v", err)
	}
	forbidden := []string{"internal/", "opscore/internal", "internal/core", "internal/plugin"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.Contains(p, bad) {
					t.Errorf("%s imports %q — SDK must stay standalone (MUST-4/5)", e.Name(), p)
				}
			}
		}
	}
}

// TestSDKServeRoundTrip proves the SDK serves a plan over stdin/stdout with no
// dependency on the host's isolation package — a third-party plugin could be
// driven by any host speaking opscore.isolation/v1.
func TestSDKServeRoundTrip(t *testing.T) {
	handlers := map[string]HandlerFunc{
		"demo.echo": func(ctx ContextProjection, input map[string]any) (*PlanWire, error) {
			return &PlanWire{
				OperationName: "demo.echo",
				Steps: []StepWire{{
					Kind:    StepKindCommand,
					Command: &CommandWire{Executable: "/bin/echo", Args: []string{ctx.TargetUser}},
				}},
			}, nil
		},
		"demo.fail": func(ContextProjection, map[string]any) (*PlanWire, error) {
			return nil, errTestBoom
		},
	}

	// happy path
	var out bytes.Buffer
	if err := Serve(newRequestReader(t, "demo.echo", map[string]any{"k": "v"},
		ContextProjection{TargetUser: "root", UserID: "alice"}), &out, handlers); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	resp := mustDecodeResponse(t, out.Bytes())
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if resp.Plan == nil || resp.Plan.OperationName != "demo.echo" {
		t.Fatalf("plan not echoed: %+v", resp.Plan)
	}
	if len(resp.Plan.Steps) != 1 || resp.Plan.Steps[0].Command.Args[0] != "root" {
		t.Fatalf("step/target-user not projected: %+v", resp.Plan.Steps)
	}

	// plugin error path: reported in Response, not as a transport error
	out.Reset()
	if err := Serve(newRequestReader(t, "demo.fail", nil, ContextProjection{}), &out, handlers); err != nil {
		t.Fatalf("Serve transport error (want none): %v", err)
	}
	resp = mustDecodeResponse(t, out.Bytes())
	if resp.Error == "" {
		t.Fatal("plugin error must be carried in the Response")
	}

	// unknown operation path: reported in Response
	out.Reset()
	if err := Serve(newRequestReader(t, "demo.nope", nil, ContextProjection{}), &out, handlers); err != nil {
		t.Fatalf("Serve transport error (want none): %v", err)
	}
	resp = mustDecodeResponse(t, out.Bytes())
	if resp.Error == "" || !strings.Contains(resp.Error, "unknown operation") {
		t.Fatalf("want unknown-operation error, got %+v", resp)
	}
}

var errTestBoom = errors.New("boom")

// newRequestReader builds a stdin stream carrying one Request frame.
func newRequestReader(t *testing.T, op string, input map[string]any, ctx ContextProjection) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(Request{Protocol: ProtocolVersion, Operation: op, Input: input, Context: ctx})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, json.RawMessage(raw)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func mustDecodeResponse(t *testing.T, b []byte) Response {
	t.Helper()
	idx := bytes.IndexByte(b, '\n')
	if idx < 0 {
		t.Fatalf("no frame header in %q", b)
	}
	var resp Response
	if err := json.Unmarshal(b[idx+1:], &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}
