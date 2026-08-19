package diskop

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// UsageHandler reports filesystem usage (df -h). Read-only, low-risk.
type UsageHandler struct{}

func NewUsageHandler() *UsageHandler { return &UsageHandler{} }

func (h *UsageHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters
	return &core.ExecutionPlan{
		OperationName: "system.disk.usage",
		Permission:    core.Permission{ResourceType: "system.disk", Action: "usage"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "disk-usage",
				Executable: "df",
				Args:       []string{"-h"},
				Timeout:    15 * time.Second,
			},
		},
	}, nil
}
