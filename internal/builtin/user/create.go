package userop

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
)

// CreateRequest is the strongly-typed input for system.user.create.
type CreateRequest struct {
	Name   string `json:"name"`   // required login name
	Group  string `json:"group"`  // optional primary group (-g)
	Shell  string `json:"shell"`  // optional login shell (-s)
	System bool   `json:"system"` // create a system account (-r)
}

// CreateHandler creates a POSIX user account (useradd). Portable across Linux
// distros; the resolver is intentionally NOT consulted because useradd/userdel
// are POSIX-standard tools, not distro-specific.
type CreateHandler struct{}

func NewCreateHandler() *CreateHandler { return &CreateHandler{} }

func (h *CreateHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[CreateRequest](input)
	if err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, fmt.Errorf("%w: missing required parameter \"name\"", core.ErrInvalidInput)
	}
	// MUST fix (GPT review): user-supplied fields become distinct argv elements.
	// A value beginning with '-' would be re-parsed as a flag by useradd
	// (e.g. "useradd -o -u 0" → root UID). Reject any such value. There is no
	// ExtraArgs escape hatch — the builtin is a strict whitelist.
	for _, f := range []string{req.Name, req.Group, req.Shell} {
		if f != "" && f != "-" {
			if err := core.SafeToken(f); err != nil {
				return nil, fmt.Errorf("%w: %v", core.ErrInvalidInput, err)
			}
		}
	}
	args := []string{"-m"}
	if req.System {
		args = append(args, "-r")
	}
	if req.Shell != "" {
		args = append(args, "-s", req.Shell)
	}
	if req.Group != "" {
		args = append(args, "-g", req.Group)
	}
	args = append(args, req.Name)

	return &core.ExecutionPlan{
		OperationName: "system.user.create",
		Permission:    core.Permission{ResourceType: "system.user", Action: "create"},
		Risk:          core.RiskHigh,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "create-user",
				Executable: "useradd",
				Args:       args,
				Timeout:    30 * time.Second,
			},
		},
	}, nil
}
