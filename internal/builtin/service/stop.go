package service

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// StopServiceRequest is the strongly-typed input for system.service.stop.
type StopServiceRequest struct {
	Name string `json:"name"`
}

// StopHandler stops a systemd service on the target host.
type StopHandler struct{}

func NewStopHandler() *StopHandler { return &StopHandler{} }

func (h *StopHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[StopServiceRequest](input)
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
		OperationName: "system.service.stop",
		Permission:    core.Permission{ResourceType: "system.service", Action: "stop"},
		Risk:          core.RiskMedium,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "stop",
				Executable: mgr,
				Args:       []string{"stop", req.Name},
				Timeout:    30 * time.Second,
			},
		},
	}
	return plan, nil
}
