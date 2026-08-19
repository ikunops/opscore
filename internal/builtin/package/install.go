package packageop

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// InstallRequest is the strongly-typed input for system.package.install.
// The handler owns this struct; the Runtime only ever passes a decoded value.
type InstallRequest struct {
	Names []string `json:"names"` // one or more package names
}

// InstallHandler installs packages via the host's resolved package manager.
type InstallHandler struct{}

func NewInstallHandler() *InstallHandler { return &InstallHandler{} }

func (h *InstallHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[InstallRequest](input)
	if err != nil {
		return nil, err
	}
	if len(req.Names) == 0 {
		return nil, fmt.Errorf("%w: missing required parameter \"names\"", core.ErrInvalidInput)
	}
	pm, args, err := platform.New(ctx).PackageCommand(platform.PkgInstall, req.Names)
	if err != nil {
		return nil, err
	}
	return &core.ExecutionPlan{
		OperationName: "system.package.install",
		Permission:    core.Permission{ResourceType: "system.package", Action: "install"},
		Risk:          core.RiskMedium,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "install-packages",
				Executable: pm,
				Args:       args,
				Timeout:    10 * time.Minute,
			},
		},
	}, nil
}
