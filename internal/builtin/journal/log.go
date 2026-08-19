package journal

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// JournalLogRequest is the strongly-typed input for system.journal.log.
type JournalLogRequest struct {
	Unit  string `json:"unit"`  // optional systemd unit, e.g. "nginx.service"
	Lines int    `json:"lines"` // number of trailing lines (default 100)
	Since string `json:"since"` // optional relative time, e.g. "1h", "2026-01-01"
}

// LogHandler reads system logs via journalctl (or the target's log reader,
// chosen by the platform resolver). Read-only, low-risk.
type LogHandler struct{}

func NewLogHandler() *LogHandler { return &LogHandler{} }

func (h *LogHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[JournalLogRequest](input)
	if err != nil {
		return nil, err
	}
	if req.Lines < 0 {
		return nil, fmt.Errorf("%w: \"lines\" must not be negative", core.ErrInvalidInput)
	}
	if req.Lines == 0 {
		req.Lines = 100
	}

	remote := !ctx.Target().IsZero()
	reader, err := platform.New(ctx).LogReader(remote)
	if err != nil {
		return nil, err
	}

	args := []string{}
	if req.Unit != "" {
		args = append(args, "-u", req.Unit)
	}
	if req.Since != "" {
		args = append(args, "--since", req.Since)
	}
	args = append(args, "-n", fmt.Sprintf("%d", req.Lines), "--no-pager")

	return &core.ExecutionPlan{
		OperationName: "system.journal.log",
		Permission:    core.Permission{ResourceType: "system.journal", Action: "read"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "journal",
				Executable: reader,
				Args:       args,
				Timeout:    15 * time.Second,
			},
		},
	}, nil
}
