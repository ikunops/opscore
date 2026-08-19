package service

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// ListHandler lists units managed by the service manager. Read-only, low-risk.
type ListHandler struct{}

func NewListHandler() *ListHandler { return &ListHandler{} }

func (h *ListHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters

	mgr, err := platform.New(ctx).ServiceManager(!ctx.Target().IsZero())
	if err != nil {
		return nil, err
	}

	return &core.ExecutionPlan{
		OperationName: "system.service.list",
		Permission:    core.Permission{ResourceType: "system.service", Action: "list"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "list-units",
				Executable: mgr,
				Args:       []string{"list-units", "--no-pager", "--no-legend"},
				Timeout:    10 * time.Second,
			},
		},
	}, nil
}
