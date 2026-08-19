package sdk

import (
	"bufio"
	"fmt"
	"io"
	"os"
)

// HandlerFunc is a third-party plugin operation. It receives the projected
// host context and the raw input, and returns a wire plan (or an error). It
// must NOT contact Manager / Registry / Catalog — those live in the host.
type HandlerFunc func(ctx ContextProjection, input map[string]any) (*PlanWire, error)

// Serve is the plugin-side half of the protocol: it reads exactly one Request
// from r, dispatches it to the matching handler, and writes one Response to w.
//
// Serve returns an error ONLY for transport failures. A plugin-level failure
// (unknown operation, handler error) is reported inside the Response, so the
// host can distinguish "the plugin said no" from "the channel broke" — they
// demand different operator responses.
//
// Serve deliberately does not recover panics into a Response. A panicking
// plugin SHOULD take its own process down: that is exactly the failure the
// host is insulated from (process isolation MUST-4), and the crash plus its
// stderr trace is more honest than a synthesised error.
func Serve(r io.Reader, w io.Writer, handlers map[string]HandlerFunc) error {
	var req Request
	if err := ReadFrame(bufio.NewReader(r), 0, &req); err != nil {
		return fmt.Errorf("opscore sdk: read request: %w", err)
	}
	if req.Protocol != ProtocolVersion {
		return WriteFrame(w, Response{
			Protocol: ProtocolVersion,
			Error: fmt.Sprintf("unsupported protocol %q, this plugin speaks %q",
				req.Protocol, ProtocolVersion),
		})
	}

	h, ok := handlers[req.Operation]
	if !ok || h == nil {
		return WriteFrame(w, Response{
			Protocol: ProtocolVersion,
			Error:    fmt.Sprintf("unknown operation %q", req.Operation),
		})
	}

	plan, err := h(req.Context, req.Input)
	if err != nil {
		return WriteFrame(w, Response{Protocol: ProtocolVersion, Error: err.Error()})
	}
	return WriteFrame(w, Response{Protocol: ProtocolVersion, Plan: plan})
}

// PluginMain is the entry point a third-party plugin's main() calls. It serves
// a single invocation over os.Stdin / os.Stdout (process-per-invocation,
// matching the host's isolation model) and exits non-zero only on a transport
// failure.
//
//	func main() {
//	    sdk.PluginMain(map[string]sdk.HandlerFunc{
//	        "hello.world": func(ctx sdk.ContextProjection, in map[string]any) (*sdk.PlanWire, error) {
//	            return &sdk.PlanWire{OperationName: "hello.world"}, nil
//	        },
//	    })
//	}
func PluginMain(handlers map[string]HandlerFunc) {
	if err := Serve(os.Stdin, os.Stdout, handlers); err != nil {
		fmt.Fprintln(os.Stderr, "opscore plugin:", err)
		os.Exit(1)
	}
}
