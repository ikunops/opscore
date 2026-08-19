package service

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// RestartServiceRequest is the strongly-typed input for system.service.restart.
// The handler owns this struct; the Runtime only ever passes it a decoded
// value (Round6: "Handler 自己拥有强类型请求 struct").
type RestartServiceRequest struct {
	Name string `json:"name"`
}

// RestartHandler builds an ExecutionPlan for restarting a system service.
// This is the first business chain — validates the entire architecture end-to-end:
// CLI → Dispatcher → Handler.Plan → ExecutionPlan → Executor → CommandStep → systemctl → AuditSink
type RestartHandler struct{}

func NewRestartHandler() *RestartHandler {
	return &RestartHandler{}
}

func (h *RestartHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[RestartServiceRequest](input)
	if err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, fmt.Errorf("%w: missing required parameter \"name\"", core.ErrInvalidInput)
	}

	// The command executable is chosen by the platform resolver from the
	// target's capability — the builtin expresses intent, not a distro tool.
	mgr, err := platform.New(ctx).ServiceManager(!ctx.Target().IsZero())
	if err != nil {
		return nil, err
	}

	plan := &core.ExecutionPlan{
		OperationName: "system.service.restart",
		Permission: core.Permission{
			ResourceType: "system.service",
			Action:       "restart",
		},
		Risk: core.RiskMedium,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "check-active",
				Executable: mgr,
				Args:       []string{"is-active", req.Name},
				Timeout:    5 * time.Second,
			},
			&core.CommandStep{
				Name:       "restart",
				Executable: mgr,
				Args:       []string{"restart", req.Name},
				Timeout:    30 * time.Second,
			},
			&core.CommandStep{
				Name:       "verify-active",
				Executable: mgr,
				Args:       []string{"is-active", req.Name},
				Timeout:    5 * time.Second,
			},
		},
	}

	return plan, nil
}
