package diskop

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// ListHandler enumerates block devices (lsblk). Read-only, low-risk.
type ListHandler struct{}

func NewListHandler() *ListHandler { return &ListHandler{} }

func (h *ListHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters
	return &core.ExecutionPlan{
		OperationName: "system.disk.list",
		Permission:    core.Permission{ResourceType: "system.disk", Action: "list"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "list-block-devices",
				Executable: "lsblk",
				Args:       []string{"-o", "NAME,SIZE,TYPE,MOUNTPOINT,FSTYPE"},
				Timeout:    15 * time.Second,
			},
		},
	}, nil
}
