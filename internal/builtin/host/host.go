package host

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// InfoHandler collects read-only host telemetry in a single multi-step plan.
// The commands (uptime / uname / free / df) are universal on Linux targets;
// parsing and presentation are left to the caller (thin control plane). Remote
// execution runs them over SSH; the executor aggregates each step's output.
type InfoHandler struct{}

func NewInfoHandler() *InfoHandler { return &InfoHandler{} }

func (h *InfoHandler) Plan(_ core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters
	return &core.ExecutionPlan{
		OperationName: "system.host.info",
		Permission:    core.Permission{ResourceType: "system.host", Action: "info"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{Name: "uptime", Executable: "uptime", Timeout: 10 * time.Second},
			&core.CommandStep{Name: "kernel", Executable: "uname", Args: []string{"-a"}, Timeout: 10 * time.Second},
			&core.CommandStep{Name: "memory", Executable: "free", Args: []string{"-h"}, Timeout: 10 * time.Second},
			&core.CommandStep{Name: "disk", Executable: "df", Args: []string{"-h", "/"}, Timeout: 10 * time.Second},
		},
	}, nil
}

// RebootHandler reboots the target host. CRITICAL risk — only callers granted
// system.host.reboot may invoke it (enforced by RBAC before dispatch).
type RebootHandler struct{}

func NewRebootHandler() *RebootHandler { return &RebootHandler{} }

func (h *RebootHandler) Plan(_ core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // no parameters
	return &core.ExecutionPlan{
		OperationName: "system.host.reboot",
		Permission:    core.Permission{ResourceType: "system.host", Action: "reboot"},
		Risk:          core.RiskCritical,
		Steps: []core.ExecutionStep{
			&core.CommandStep{Name: "reboot", Executable: "reboot", Timeout: 30 * time.Second},
		},
	}, nil
}
