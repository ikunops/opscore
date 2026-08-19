package inventory

import (
	"errors"

	"github.com/YuDong999/opscore/internal/core"
)

// ErrOpUnauthorized is returned by a Runner when the caller may not invoke the
// requested read-only op. Collect marks that op's result as Skipped rather than
// Failed, so a partially-authorized caller still sees everything they're
// allowed to — inventory degrades gracefully instead of erroring out.
var ErrOpUnauthorized = errors.New("op unauthorized")

// ReadOnlyOp is a whitelisted read-only operation whose output becomes part of
// the inventory detail view. Membership is EXPLICIT (NOT "every low-risk op")
// so a future mutating op mistakenly flagged low-risk can never leak its output
// into the read-only inventory surface. This whitelist is the contract for
// "what real data does inventory show".
type ReadOnlyOp struct {
	Name    string // registered operation name, e.g. "system.disk.mounts"
	Title   string // human label for UI rendering
	Section string // stable key for UI grouping / ordering
}

// ReadOnlyWhitelist is the canonical set of read-only ops surfaced in the
// inventory detail. All are RiskLow and must be registered builtins. The order
// here is the display order.
//
// Phase 2.9 closure: these ops already existed as builtins (system.host.info,
// system.disk.mounts, system.package.list, system.user.list, system.service.list,
// system.process.list, system.disk.list) but were never surfaced as inventory
// data — inventory only projected the Host/Capability snapshot. This whitelist
// is what turns "we have read-only ops" into "the inventory shows real data".
var ReadOnlyWhitelist = []ReadOnlyOp{
	{Name: "system.host.info", Title: "Host Info", Section: "host_info"},
	{Name: "system.disk.mounts", Title: "Mounts", Section: "mounts"},
	{Name: "system.disk.list", Title: "Disks", Section: "disks"},
	{Name: "system.package.list", Title: "Packages", Section: "packages"},
	{Name: "system.user.list", Title: "Users", Section: "users"},
	{Name: "system.service.list", Title: "Services", Section: "services"},
	{Name: "system.process.list", Title: "Processes", Section: "processes"},
}

// Runner executes a single read-only op and returns its non-empty step outputs
// in order. The production implementation runs the op through the existing
// Execution stack (Dispatcher.Plan -> Runtime.Run -> Executor -> Audit) so the
// inventory NEVER bypasses the SSOT; tests inject a fake.
type Runner func(ctx core.Context, opName string) ([]string, error)

// OpResult is one read-only op's contribution to the inventory detail.
type OpResult struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Ok      bool     `json:"ok"`
	Skipped bool     `json:"skipped,omitempty"`
	Error   string   `json:"error,omitempty"`
	Steps   []string `json:"steps,omitempty"`
}

// Detail is the aggregated read-only observation data — the "user value" GPT
// asked for in the Phase 2.9 review: real output from the read-only ops, not
// just the host identity/capability snapshot.
type Detail struct {
	Target string     `json:"target,omitempty"`
	Ops    []OpResult `json:"ops"`
}

// Collect runs the read-only whitelist through runner and aggregates the
// results into a Detail. It NEVER errors: per-op failures are recorded in the
// OpResult (Ok=false with the error message) and an unauthorized op is marked
// Skipped, so a partially-authorized or unreachable target still yields a
// useful (degraded) inventory instead of a hard failure.
func Collect(ctx core.Context, runner Runner) *Detail {
	d := &Detail{}
	if h := ctx.HostSnapshot(); h != nil {
		d.Target = h.ID
	}
	for _, op := range ReadOnlyWhitelist {
		res := OpResult{Name: op.Name, Title: op.Title}
		steps, err := runner(ctx, op.Name)
		if err != nil {
			if errors.Is(err, ErrOpUnauthorized) {
				res.Skipped = true
				res.Error = "unauthorized"
			} else {
				res.Ok = false
				res.Error = err.Error()
			}
		} else {
			res.Ok = true
			res.Steps = steps
		}
		d.Ops = append(d.Ops, res)
	}
	return d
}
