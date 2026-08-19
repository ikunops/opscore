// Package sdk is the public, dependency-free reference implementation of the
// OpsCore Executable Plugin wire protocol (opscore.isolation/v1).
//
// It is the single source of truth for the wire types and framing used by both
// the in-repo process-isolation helper (internal/plugin/isolation) and
// third-party plugin binaries. A plugin author imports ONLY this package and
// implements HandlerFunc; they never touch core, runtime, Manager, Registry,
// Reload, Watcher or Catalog. The plugin talks to the host over stdin/stdout
// using length-prefixed JSON frames — that is the entire contract.
//
// Design rules enforced by Phase 7.1 (GPT Round 30 directive):
//
//	MUST-1  This package never changes the opscore.isolation/v1 wire format.
//	MUST-2  It never imports the Runtime Contract (core / runtime / ...) — the
//	        protocol is standalone.
//	MUST-3  It is a Reference Implementation of the protocol, not a second one.
//	MUST-4  It compiles on its own (stdlib only) so a plugin repo can
//	        `go build` straight to a binary.
//	MUST-5  It knows only stdin/stdout and the protocol; it has no concept of
//	        Manager / Registry / Reload / Watcher / Catalog.
//
// Package sdk lives OUTSIDE internal/ precisely so external plugin modules can
// import it.
package sdk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ProtocolVersion identifies the stdio wire format. The plugin rejects a
// request whose protocol it does not understand, rather than guessing.
const ProtocolVersion = "opscore.isolation/v1"

// frameMagic prefixes every frame: "<magic> <byteLen>\n" followed by exactly
// byteLen bytes of JSON. Length prefixing (instead of newline-delimited JSON)
// means a response can be size-capped BEFORE it is read into memory, and a
// plugin that scribbles on stdout desynchronises the frame instead of silently
// injecting a plan.
const frameMagic = "OPSCORE-ISO/1"

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// ContextProjection is the serializable subset of host context handed to a
// plugin.
//
// Two deliberate omissions (the same security properties as the in-repo
// isolation layer):
//
//   - Live objects (loggers, DB handles, the cancellation context) cannot cross
//     a process boundary and are not faked. Cancellation is expressed by killing
//     the process, not by a wire field.
//
//   - CREDENTIALS ARE STRIPPED. Only Address / Port / User of a target cross;
//     Password / KeyPath / KeyBytes never do.
//
// CapabilitySnapshot and HostSnapshot are carried as opaque JSON so this
// package can stay free of the host's snapshot types (MUST-2 / MUST-4): the
// host projects them read-only, the plugin receives exactly what was sent and
// must never re-detect. They use `omitempty`, so an old helper that does not
// understand them simply stays Capability-blind (the honest default).
type ContextProjection struct {
	UserID   string `json:"userId,omitempty"`
	UserName string `json:"userName,omitempty"`
	UserRole string `json:"userRole,omitempty"`

	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`

	TargetAddress string `json:"targetAddress,omitempty"`
	TargetPort    int    `json:"targetPort,omitempty"`
	TargetUser    string `json:"targetUser,omitempty"`

	TraceID string `json:"traceId,omitempty"`
	// RequestID is the host-side execution / request id, projected so an
	// isolated plan can be correlated with the driving Execution record across
	// the process hop.
	RequestID string `json:"requestId,omitempty"`

	// CapabilitySnapshot is the HOST-OBSERVED capability snapshot for the
	// current target, projected read-only (opaque JSON). Empty means "the host
	// did not project one" — the plugin must then stay Capability-blind and
	// MUST NOT detect its own machine's capabilities (that would be the
	// plugin's, silently wrong for a remote target).
	CapabilitySnapshot json.RawMessage `json:"capabilitySnapshot,omitempty"`
	// HostSnapshot is the HOST-OBSERVED identity for the current target,
	// projected read-only (opaque JSON).
	HostSnapshot json.RawMessage `json:"hostSnapshot,omitempty"`
}

// Request is one Plan invocation sent host -> plugin.
type Request struct {
	Protocol  string            `json:"protocol"`
	Operation string            `json:"operation"`
	Input     map[string]any    `json:"input,omitempty"`
	Context   ContextProjection `json:"context"`
}

// Response is the plugin's answer. Exactly one of Plan / Error is set.
type Response struct {
	Protocol string    `json:"protocol"`
	Plan     *PlanWire `json:"plan,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// PlanWire is the serialized form of an execution plan.
type PlanWire struct {
	OperationName string     `json:"operationName"`
	Steps         []StepWire `json:"steps,omitempty"`
	Permission    struct {
		ResourceType string `json:"resourceType,omitempty"`
		Action       string `json:"action,omitempty"`
	} `json:"permission"`
	Risk         int   `json:"risk"`
	TimeoutNanos int64 `json:"timeoutNanos,omitempty"`
	Source       string
	Origin       string
}

// StepWire is a discriminated union over step kinds. Only kinds with a declared
// wire form can cross the boundary; an unknown kind is a hard error, never a
// silently dropped step.
type StepWire struct {
	Kind    string       `json:"kind"`
	Command *CommandWire `json:"command,omitempty"`
}

// StepKindCommand is the only kind defined in v1.
const StepKindCommand = "command"

// CommandWire is the wire form of a command step.
type CommandWire struct {
	Name         string            `json:"name,omitempty"`
	ID           string            `json:"id,omitempty"`
	Index        int               `json:"index,omitempty"`
	Executable   string            `json:"executable"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	WorkingDir   string            `json:"workingDir,omitempty"`
	TimeoutNanos int64             `json:"timeoutNanos,omitempty"`
}

// ---------------------------------------------------------------------------
// Framing
// ---------------------------------------------------------------------------

// WriteFrame length-prefixes and writes one JSON value.
func WriteFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("opscore sdk: marshal frame: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s %d\n", frameMagic, len(body)); err != nil {
		return fmt.Errorf("opscore sdk: write frame header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("opscore sdk: write frame body: %w", err)
	}
	return nil
}

// ReadFrame reads one length-prefixed JSON value. maxBytes (0 = unlimited)
// caps the declared body length BEFORE any allocation, so a plugin cannot
// exhaust host memory by announcing a huge frame.
func ReadFrame(r *bufio.Reader, maxBytes int64, v any) error {
	header, err := r.ReadString('\n')
	if err != nil {
		return err // io.EOF here means "plugin produced no response"
	}
	fields := strings.Fields(strings.TrimSpace(header))
	if len(fields) != 2 || fields[0] != frameMagic {
		return fmt.Errorf("opscore sdk: malformed frame header %q", strings.TrimSpace(header))
	}
	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("opscore sdk: bad frame length %q", fields[1])
	}
	if maxBytes > 0 && n > maxBytes {
		return fmt.Errorf("opscore sdk: frame of %d bytes exceeds the %d byte limit", n, maxBytes)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return fmt.Errorf("opscore sdk: short frame body: %w", err)
	}
	return json.Unmarshal(body, v)
}
