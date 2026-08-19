package management

import (
	"fmt"
	"strings"

	"github.com/YuDong999/opscore/internal/storage"
)

// The frozen policy.* Action vocabulary (ADR-036 §3.3.3, R75 §4). These five
// values are the complete admitted set. A policy mutation MUST NOT be recorded
// as "execute" or "plan" — that conflation is precisely what P17-10 exists to
// prevent, and after this vocabulary exists the two are separable in the audit
// table by a single indexed column.
const (
	ActionCreate     = "policy.create"
	ActionUpdate     = "policy.update"
	ActionActivate   = "policy.activate"
	ActionDeactivate = "policy.deactivate"
	ActionArchive    = "policy.archive"
)

// Audit Result classes (ADR-036 §3.3.2.1, frozen at R76/R77).
//
// There is deliberately no "conflict" class: a CAS refusal is a resultFailure
// whose Detail carries the reason and whose Revision carries the actually
// stored value. Adding a fourth class would widen a signed vocabulary and would
// break every existing reader that treats "not success" as failure.
const (
	resultIntent  = "intent"
	resultSuccess = "success"
	resultFailure = "failure"
)

// auditOperation is the Operation column for every Management row. A single
// stable value makes ListByOperation(auditOperation) return the entire
// management trail; the specific verb already lives in Action, so repeating it
// here would buy nothing and cost the ability to query the surface as a whole.
const auditOperation = "policy-management"

// revisionUnknown marks an audit row whose revision could not be established.
// It is written explicitly instead of defaulting to 0, because 0 is a MEANING
// in this contract ("must not already exist") and silently reusing it would
// forge a fact.
const revisionUnknown = -1

// auditRecord is the management view of an audit row. It exists so the handler
// never hand-assembles a storage.AuditEvent and cannot forget a frozen field.
type auditRecord struct {
	actor         string
	action        string
	policyID      string
	result        string
	revision      int
	correlationID string
	detail        string
}

// event projects onto the storage row. Timestamp is left zero on purpose: the
// store stamps it at insert time, so the recorded instant is the durable one
// rather than one the caller could skew.
//
// CapabilityHash / ExecutionID / SnapshotSchemaVersion stay empty. ExecutionID
// in particular is NEVER cross-populated with CorrelationID (ADR-036 §3.3.2.1):
// they are independent identifiers, and joining them would manufacture a causal
// link between a policy mutation and an execution that does not exist.
func (a auditRecord) event() storage.AuditEvent {
	return storage.AuditEvent{
		Actor:         a.actor,
		Operation:     auditOperation,
		Action:        a.action,
		Target:        a.policyID,
		Result:        a.result,
		Detail:        a.detail,
		Revision:      a.revision,
		CorrelationID: a.correlationID,
	}
}

// detailf builds the Detail string as ordered key=value pairs. Deterministic
// and greppable in SQL, which is what an operator reconstructing a chain from
// the table alone actually needs.
func detailf(pairs ...string) string {
	if len(pairs)%2 != 0 {
		pairs = append(pairs, "")
	}
	var b strings.Builder
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(pairs[i])
		b.WriteByte('=')
		b.WriteString(pairs[i+1])
	}
	return b.String()
}

func itoa(n int) string {
	if n == revisionUnknown {
		return "unknown"
	}
	return fmt.Sprintf("%d", n)
}
