package packageop

import (
	"time"

	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// UpdateHandler refreshes the package manager's metadata cache
// (apt update / dnf makecache / apk update / ...). Read-modify; medium risk.
type UpdateHandler struct{}

func NewUpdateHandler() *UpdateHandler { return &UpdateHandler{} }

func (h *UpdateHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-modify, no parameters
	pm, args, err := platform.New(ctx).PackageCommand(platform.PkgUpdate, nil)
	if err != nil {
		return nil, err
	}
	return &core.ExecutionPlan{
		OperationName: "system.package.update",
		Permission:    core.Permission{ResourceType: "system.package", Action: "update"},
		Risk:          core.RiskMedium,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "update-package-cache",
				Executable: pm,
				Args:       args,
				Timeout:    5 * time.Minute,
			},
		},
	}, nil
}
