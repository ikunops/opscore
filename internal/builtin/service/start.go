package service

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// StartServiceRequest is the strongly-typed input for system.service.start.
type StartServiceRequest struct {
	Name string `json:"name"`
}

// StartHandler starts a systemd service on the target host.
type StartHandler struct{}

func NewStartHandler() *StartHandler { return &StartHandler{} }

func (h *StartHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[StartServiceRequest](input)
	if err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, fmt.Errorf("%w: missing required parameter \"name\"", core.ErrInvalidInput)
	}

	mgr, err := platform.New(ctx).ServiceManager(!ctx.Target().IsZero())
	if err != nil {
		return nil, err
	}

	plan := &core.ExecutionPlan{
		OperationName: "system.service.start",
		Permission:    core.Permission{ResourceType: "system.service", Action: "start"},
		Risk:          core.RiskMedium,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "start",
				Executable: mgr,
				Args:       []string{"start", req.Name},
				Timeout:    30 * time.Second,
			},
		},
	}
	return plan, nil
}
