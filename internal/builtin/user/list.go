package userop

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// ListHandler lists user accounts via getent passwd (read-only, low-risk).
type ListHandler struct{}

func NewListHandler() *ListHandler { return &ListHandler{} }

func (h *ListHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters
	return &core.ExecutionPlan{
		OperationName: "system.user.list",
		Permission:    core.Permission{ResourceType: "system.user", Action: "list"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "list-users",
				Executable: "getent",
				Args:       []string{"passwd"},
				Timeout:    15 * time.Second,
			},
		},
	}, nil
}
