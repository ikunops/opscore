package packageop

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// ListHandler lists installed packages (read-only, low-risk).
type ListHandler struct{}

func NewListHandler() *ListHandler { return &ListHandler{} }

func (h *ListHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters
	pm, args, err := platform.New(ctx).PackageCommand(platform.PkgList, nil)
	if err != nil {
		return nil, err
	}
	return &core.ExecutionPlan{
		OperationName: "system.package.list",
		Permission:    core.Permission{ResourceType: "system.package", Action: "list"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "list-packages",
				Executable: pm,
				Args:       args,
				Timeout:    30 * time.Second,
			},
		},
	}, nil
}
