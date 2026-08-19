package isolation

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/YuDong999/opscore/ecosystem/sdk"
	"github.com/YuDong999/opscore/internal/core"
)

// Sentinel errors. Every one of them means "no plan was produced" — the
// isolation layer is fail-closed on all paths (MUST-5).
var (
	// ErrHelperTimeout means the helper exceeded ExecTimeout and WAS KILLED.
	// Unlike the Phase 6.1 envelope timeout, this is real termination: the
	// plugin's goroutines die with its process (MUST-3).
	ErrHelperTimeout = errors.New("isolation: helper process timed out and was killed")
	// ErrHelperCrashed means the helper died or exited non-zero.
	ErrHelperCrashed = errors.New("isolation: helper process crashed")
	// ErrProtocol means the helper spoke something we do not understand.
	ErrProtocol = errors.New("isolation: protocol violation")
	// ErrPluginFailed means the plugin ran and reported a normal error.
	ErrPluginFailed = errors.New("isolation: plugin returned an error")
)

// Code is a machine-readable outcome, mirroring the Phase 6.1 DecisionCode /
// CompatibilityResult / SignatureResult convention so audit, metrics and UI
// never have to parse a message string.
type Code string

const (
	CodeOK               Code = "ok"
	CodeSpawnFailed      Code = "spawn-failed"
	CodeTimeoutKilled    Code = "timeout-killed"
	CodeHelperCrash      Code = "helper-crash"
	CodeProtocolError    Code = "protocol-error"
	CodePluginError      Code = "plugin-error"
	CodeResponseTooLarge Code = "response-too-large"
	CodeUnserializable   Code = "unserializable-plan"
)

// Event is the peripheral observation record for one isolated invocation.
// As in Phase 6.1, this is an observer hook, NOT the Runtime Audit Contract.
type Event struct {
	Operation string
	Code      Code
	Reason    string
	Duration  time.Duration
	Killed    bool
	ExitCode  int
	// Stderr is the helper's captured stderr, truncated. Plugin logs and panic
	// traces land here, which is how a crash stays diagnosable even though it
	// can no longer take the host down.
	Stderr string
}

// Config describes how to launch and bound one plugin helper process.
type Config struct {
	// Path is the helper executable; Args/Env/Dir are passed through.
	Path string
	Args []string
	Env  []string
	Dir  string

	// ExecTimeout bounds the whole invocation. On expiry the process is
	// KILLED. 0 selects DefaultExecTimeout; a negative value means unbounded
	// (discouraged: it gives up the one guarantee 6.3 exists to provide).
	ExecTimeout time.Duration

	// MaxResponseBytes caps the declared response frame size. 0 selects
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64

	// MaxStderrBytes caps captured stderr. 0 selects DefaultMaxStderrBytes.
	MaxStderrBytes int

	// AuditSink observes outcomes. Never nil-checked by callers; may be nil.
	AuditSink func(Event)
}

// Defaults chosen to be safe rather than generous.
const (
	DefaultExecTimeout      = 30 * time.Second
	DefaultMaxResponseBytes = 8 << 20 // 8 MiB
	DefaultMaxStderrBytes   = 64 << 10
)

// NewHandler returns a core.Handler that plans `operation` in a helper
// process. The returned value satisfies core.Handler and nothing more, so
// Manager / Registry / Reload / Watcher cannot tell it apart from an
// in-process handler (MUST-2), and Handler.Plan keeps its signature (MUST-1).
//
// One process is spawned per invocation. That is deliberate for 6.3: a fresh
// process means no state leaks between operations and no pool lifecycle to get
// wrong. A long-lived pooled helper is a performance optimisation that can be
// layered on later without changing this surface.
func NewHandler(operation string, cfg Config) core.Handler {
	return &processHandler{operation: operation, cfg: cfg}
}

type processHandler struct {
	operation string
	cfg       Config
}

func (h *processHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	start := time.Now()

	timeout := h.cfg.ExecTimeout
	if timeout == 0 {
		timeout = DefaultExecTimeout
	}
	maxResp := h.cfg.MaxResponseBytes
	if maxResp == 0 {
		maxResp = DefaultMaxResponseBytes
	}
	maxErr := h.cfg.MaxStderrBytes
	if maxErr == 0 {
		maxErr = DefaultMaxStderrBytes
	}

	// The parent context is honoured too, so an operator cancelling the
	// execution also kills the helper.
	base := context.Context(ctx)
	if base == nil {
		base = context.Background()
	}
	runCtx := base
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(base, timeout)
		defer cancel()
	}

	// exec.CommandContext kills the process when runCtx is done; WaitDelay
	// then forces the pipes shut so a wedged child cannot make Wait hang
	// forever. Together these are what make MUST-3 real termination rather
	// than "the caller stopped waiting".
	cmd := exec.CommandContext(runCtx, h.cfg.Path, h.cfg.Args...)
	cmd.Env = h.cfg.Env
	cmd.Dir = h.cfg.Dir
	cmd.WaitDelay = 2 * time.Second

	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: maxErr}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, h.fail(start, CodeSpawnFailed, "", fmt.Errorf("%w: stdin pipe: %v", ErrHelperCrashed, err))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, h.fail(start, CodeSpawnFailed, "", fmt.Errorf("%w: stdout pipe: %v", ErrHelperCrashed, err))
	}
	if err := cmd.Start(); err != nil {
		return nil, h.fail(start, CodeSpawnFailed, stderr.String(),
			fmt.Errorf("isolation: cannot start helper %q: %w", h.cfg.Path, err))
	}

	// From here on the process exists, so every return path must Wait to reap
	// it. reap() is called exactly once, by the deferred cleanup below.
	var (
		resp     sdk.Response
		readErr  error
		waitErr  error
		finished bool
	)
	defer func() {
		if !finished {
			_ = cmd.Wait()
		}
	}()

	reqErr := sdk.WriteFrame(stdin, sdk.Request{
		Protocol:  sdk.ProtocolVersion,
		Operation: h.operation,
		Input:     input,
		Context:   ProjectContext(ctx),
	})
	_ = stdin.Close() // signals end-of-request even if the write failed

	if reqErr == nil {
		readErr = sdk.ReadFrame(bufio.NewReader(stdout), maxResp, &resp)
	}

	waitErr = cmd.Wait()
	finished = true

	killed := runCtx.Err() != nil
	exitCode := cmd.ProcessState.ExitCode()
	errText := stderr.String()

	// Order matters: a timeout explains every downstream symptom (truncated
	// read, non-zero exit), so it is classified first.
	switch {
	case killed:
		return nil, h.failFull(start, CodeTimeoutKilled, errText, true, exitCode,
			fmt.Errorf("%w after %s", ErrHelperTimeout, timeout))

	case reqErr != nil:
		return nil, h.failFull(start, CodeHelperCrash, errText, false, exitCode,
			fmt.Errorf("%w: sending request: %v", ErrHelperCrashed, reqErr))

	case readErr != nil:
		// A helper that dies before answering shows up here as EOF; report it
		// as a crash, with its stderr, instead of a bare protocol error.
		code := CodeProtocolError
		wrapped := fmt.Errorf("%w: %v", ErrProtocol, readErr)
		if waitErr != nil || exitCode != 0 {
			code = CodeHelperCrash
			wrapped = fmt.Errorf("%w: exit %d: %v", ErrHelperCrashed, exitCode, readErr)
		}
		return nil, h.failFull(start, code, errText, false, exitCode, wrapped)

	case waitErr != nil:
		return nil, h.failFull(start, CodeHelperCrash, errText, false, exitCode,
			fmt.Errorf("%w: %v", ErrHelperCrashed, waitErr))

	case resp.Protocol != sdk.ProtocolVersion:
		return nil, h.failFull(start, CodeProtocolError, errText, false, exitCode,
			fmt.Errorf("%w: response protocol %q, want %q", ErrProtocol, resp.Protocol, sdk.ProtocolVersion))

	case resp.Error != "":
		return nil, h.failFull(start, CodePluginError, errText, false, exitCode,
			fmt.Errorf("%w: %s", ErrPluginFailed, resp.Error))

	case resp.Plan == nil:
		return nil, h.failFull(start, CodeProtocolError, errText, false, exitCode,
			fmt.Errorf("%w: response carried neither plan nor error", ErrProtocol))
	}

	plan, err := DecodePlan(resp.Plan)
	if err != nil {
		return nil, h.failFull(start, CodeUnserializable, errText, false, exitCode,
			fmt.Errorf("%w: %v", ErrProtocol, err))
	}

	h.emit(Event{
		Operation: h.operation,
		Code:      CodeOK,
		Duration:  time.Since(start),
		ExitCode:  exitCode,
		Stderr:    errText,
	})
	return plan, nil
}

func (h *processHandler) fail(start time.Time, code Code, stderr string, err error) error {
	return h.failFull(start, code, stderr, false, -1, err)
}

func (h *processHandler) failFull(start time.Time, code Code, stderr string, killed bool, exit int, err error) error {
	h.emit(Event{
		Operation: h.operation,
		Code:      code,
		Reason:    err.Error(),
		Duration:  time.Since(start),
		Killed:    killed,
		ExitCode:  exit,
		Stderr:    stderr,
	})
	return err
}

func (h *processHandler) emit(e Event) {
	if h.cfg.AuditSink != nil {
		h.cfg.AuditSink(e)
	}
}

// limitedWriter keeps at most `remaining` bytes and silently discards the
// rest, so a chatty or looping helper cannot balloon host memory through
// stderr. Discarded output must never surface as a write error, or a noisy
// plugin would look like a broken pipe.
type limitedWriter struct {
	w         *bytes.Buffer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining > 0 {
		n := len(p)
		if n > l.remaining {
			n = l.remaining
		}
		l.w.Write(p[:n])
		l.remaining -= n
	}
	return len(p), nil
}
