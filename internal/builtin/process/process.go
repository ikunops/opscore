package process

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// ListHandler lists running processes sorted by CPU usage.
// Read-only, low-risk. The raw `ps` output is returned for the caller to parse.
type ListHandler struct{}

func NewListHandler() *ListHandler { return &ListHandler{} }

func (h *ListHandler) Plan(_ core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // no parameters
	return &core.ExecutionPlan{
		OperationName: "system.process.list",
		Permission:    core.Permission{ResourceType: "system.process", Action: "list"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "ps",
				Executable: "ps",
				Args:       []string{"-eo", "pid,user,comm,%cpu,%mem,etime", "--sort=-%cpu"},
				Timeout:    10 * time.Second,
			},
		},
	}, nil
}

// KillHandler terminates a process by PID.
// Requires a positive-integer "pid"; an optional "signal" (default TERM) may be
// given (e.g. "9"/"KILL"). HIGH risk — RBAC-gated and audited like every op.
type KillHandler struct{}

func NewKillHandler() *KillHandler { return &KillHandler{} }

func (h *KillHandler) Plan(_ core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	pid, ok := toPositiveInt(input["pid"])
	if !ok {
		return nil, fmt.Errorf("%w: \"pid\" must be a positive integer", core.ErrInvalidInput)
	}

	signal := "TERM"
	if s, ok := input["signal"].(string); ok && s != "" {
		signal = normalizeSignal(s)
	}

	return &core.ExecutionPlan{
		OperationName: "system.process.kill",
		Permission:    core.Permission{ResourceType: "system.process", Action: "kill"},
		Risk:          core.RiskHigh,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "kill",
				Executable: "kill",
				Args:       []string{"-" + signal, fmt.Sprintf("%d", pid)},
				Timeout:    10 * time.Second,
			},
		},
	}, nil
}

// toPositiveInt accepts the pid from either channel: JSON numbers decode to
// float64, while the CLI passes everything as a string. Both must yield a
// strictly positive integer.
func toPositiveInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n > 0 && n == float64(int(n)) {
			return int(n), true
		}
	case int:
		if n > 0 {
			return n, true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && i > 0 {
			return i, true
		}
	}
	return 0, false
}

// normalizeSignal maps common names/numbers to the form kill expects
// ("-<signame|signum>"). Unknown values pass through unchanged.
func normalizeSignal(s string) string {
	switch s {
	case "9", "KILL", "kill", "SIGKILL":
		return "9"
	case "15", "TERM", "term", "SIGTERM":
		return "TERM"
	case "2", "INT", "int", "SIGINT":
		return "INT"
	case "1", "HUP", "hup", "SIGHUP":
		return "HUP"
	default:
		return s
	}
}
