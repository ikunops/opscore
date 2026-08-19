package diskop

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// MountsHandler lists active mounts (findmnt). Read-only, low-risk.
type MountsHandler struct{}

func NewMountsHandler() *MountsHandler { return &MountsHandler{} }

func (h *MountsHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters
	return &core.ExecutionPlan{
		OperationName: "system.disk.mounts",
		Permission:    core.Permission{ResourceType: "system.disk", Action: "mounts"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "list-mounts",
				Executable: "findmnt",
				Args:       []string{"-o", "TARGET,SOURCE,FSTYPE,OPTIONS"},
				Timeout:    15 * time.Second,
			},
		},
	}, nil
}
