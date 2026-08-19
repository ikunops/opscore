package userop

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// DeleteRequest is the strongly-typed input for system.user.delete.
type DeleteRequest struct {
	Name      string `json:"name"`      // required login name
	Recursive bool   `json:"recursive"` // also remove home dir (-r)
}

// DeleteHandler deletes a POSIX user account (userdel). High-risk operation.
type DeleteHandler struct{}

func NewDeleteHandler() *DeleteHandler { return &DeleteHandler{} }

func (h *DeleteHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[DeleteRequest](input)
	if err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, fmt.Errorf("%w: missing required parameter \"name\"", core.ErrInvalidInput)
	}
	args := []string{}
	if req.Recursive {
		args = append(args, "-r")
	}
	args = append(args, req.Name)

	return &core.ExecutionPlan{
		OperationName: "system.user.delete",
		Permission:    core.Permission{ResourceType: "system.user", Action: "delete"},
		Risk:          core.RiskHigh,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "delete-user",
				Executable: "userdel",
				Args:       args,
				Timeout:    30 * time.Second,
			},
		},
	}, nil
}
