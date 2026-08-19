package firewall

import (
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/builtin"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/platform"
)

// FirewallRuleRequest is the strongly-typed input for firewall.rule.add/remove.
// Builtins express the business intent (open/close a port); the platform
// resolver decides which tool (firewall-cmd / ufw / iptables) realizes it.
type FirewallRuleRequest struct {
	Port     int    `json:"port"`     // required, 1..65535
	Protocol string `json:"protocol"` // tcp | udp (default tcp)
	Action   string `json:"action"`   // allow | deny (default allow; only used by add)
	Source   string `json:"source"`   // optional CIDR, e.g. "10.0.0.0/8"
}

func (r *FirewallRuleRequest) normalize() error {
	if r.Port <= 0 || r.Port > 65535 {
		return fmt.Errorf("%w: \"port\" must be between 1 and 65535", core.ErrInvalidInput)
	}
	switch r.Protocol {
	case "", "tcp", "udp":
		if r.Protocol == "" {
			r.Protocol = "tcp"
		}
	default:
		return fmt.Errorf("%w: \"protocol\" must be tcp or udp", core.ErrInvalidInput)
	}
	switch r.Action {
	case "", "allow", "deny":
		if r.Action == "" {
			r.Action = "allow"
		}
	default:
		return fmt.Errorf("%w: \"action\" must be allow or deny", core.ErrInvalidInput)
	}
	return nil
}

// buildRuleArgs assembles the tool-specific argument vector. The builtin owns
// the business meaning; the exact flags are a property of the resolved tool.
func buildRuleArgs(tool string, req FirewallRuleRequest, remove bool) []string {
	switch tool {
	case "firewall-cmd":
		verb := "--add-port"
		if remove {
			verb = "--remove-port"
		}
		return []string{verb, fmt.Sprintf("%d/%s", req.Port, req.Protocol)}
	case "ufw":
		parts := []string{}
		if remove {
			parts = append(parts, "delete", "allow")
		} else {
			parts = append(parts, "allow")
		}
		if req.Source != "" {
			parts = append(parts, "from", req.Source, "to", "any", "port", fmt.Sprintf("%d", req.Port), "proto", req.Protocol)
		} else {
			parts = append(parts, fmt.Sprintf("%d/%s", req.Port, req.Protocol))
		}
		return parts
	case "iptables":
		chainVerb := "-A"
		if remove {
			chainVerb = "-D"
		}
		args := []string{chainVerb, "INPUT", "-p", req.Protocol, "--dport", fmt.Sprintf("%d", req.Port)}
		if req.Source != "" {
			args = append(args, "-s", req.Source)
		}
		if remove {
			args = append(args, "-j", "ACCEPT")
		} else {
			j := "ACCEPT"
			if req.Action == "deny" {
				j = "DROP"
			}
			args = append(args, "-j", j)
		}
		return args
	}
	return nil
}

// RuleHandler implements firewall.rule.add (remove=false) and
// firewall.rule.remove (remove=true). One Operation per authorized action.
type RuleHandler struct{ remove bool }

func NewAddHandler() *RuleHandler    { return &RuleHandler{remove: false} }
func NewRemoveHandler() *RuleHandler { return &RuleHandler{remove: true} }

func (h *RuleHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	req, err := builtin.DecodeInput[FirewallRuleRequest](input)
	if err != nil {
		return nil, err
	}
	if err := req.normalize(); err != nil {
		return nil, err
	}

	remote := !ctx.Target().IsZero()
	tool, err := platform.New(ctx).PacketFilter(remote)
	if err != nil {
		return nil, err
	}

	action := "add"
	if h.remove {
		action = "remove"
	}

	return &core.ExecutionPlan{
		OperationName: "firewall.rule." + action,
		Permission:    core.Permission{ResourceType: "firewall.rule", Action: action},
		Risk:          core.RiskHigh,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       action + "-rule",
				Executable: tool,
				Args:       buildRuleArgs(tool, req, h.remove),
				Timeout:    15 * time.Second,
			},
		},
	}, nil
}

// ListHandler lists the active firewall rules (read-only, low-risk).
type ListHandler struct{}

func NewListHandler() *ListHandler { return &ListHandler{} }

func (h *ListHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	_ = input // read-only, no parameters

	remote := !ctx.Target().IsZero()
	tool, err := platform.New(ctx).PacketFilter(remote)
	if err != nil {
		return nil, err
	}

	var args []string
	switch tool {
	case "firewall-cmd":
		args = []string{"--list-all"}
	case "ufw":
		args = []string{"status"}
	default: // iptables
		args = []string{"-L", "-n"}
	}

	return &core.ExecutionPlan{
		OperationName: "firewall.rule.list",
		Permission:    core.Permission{ResourceType: "firewall.rule", Action: "list"},
		Risk:          core.RiskLow,
		Steps: []core.ExecutionStep{
			&core.CommandStep{
				Name:       "list-rules",
				Executable: tool,
				Args:       args,
				Timeout:    10 * time.Second,
			},
		},
	}, nil
}
