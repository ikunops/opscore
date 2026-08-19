package packageop

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// RemoveRequest is the strongly-typed input for system.package.remove.
type RemoveRequest struct {
	Names []string `json:"names"`
}

// RemoveHandler removes packages. High-risk: can break dependencies, so the
// plan is marked RiskHigh and the RBAC layer must explicitly authorize it.
type RemoveHandler struct{}

func NewRemoveHandler() *RemoveHandler { return &RemoveHandler{} }

func (h *RemoveHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[RemoveRequest](input)
	if err != nil {
		return nil, err
	}
	if len(req.Names) == 0 {
		return nil, fmt.Errorf("%w: missing required parameter \"names\"", core.ErrInvalidInput)
	}
	pm, args, err := platform.New(ctx).PackageCommand(platform.PkgRemove, req.Names)
	if err != nil {
		return nil, err
	}
	return &core.ExecutionPlan{
		OperationName: "system.package.remove",
		Permission:    core.Permission{ResourceType: "system.package", Action: "remove"},
		Risk:          core.RiskHigh,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "remove-packages",
				Executable: pm,
				Args:       args,
				Timeout:    10 * time.Minute,
			},
		},
	}, nil
}
