package memory

import (
	"sync"

	"github.com/YuDong999/opscore/internal/protection"
)

// QuotaEvidenceReader is the default in-process quota consumption reader
// (R21-12 / R23-3). It is EVIDENCE ONLY — never the authoritative source; the
// live telemetry path (RemoteExecution resource samples or the auditor's
// resource fields) is expected to populate it. Until that wiring lands, every
// reading is Unknown (Complete=false) so the Gate fails CLOSED per R23-1
// (evidence unavailable ⇒ reject, never substitute zero/default — R23-4).
//
// This is intentionally a non-persistent, best-effort source: consumption is a
// point-in-time observation, not a fact QuotaStore owns.
type QuotaEvidenceReader struct {
	mu    sync.RWMutex
	usage map[string]protection.QuotaUsage // key: capability + "\x00" + principal
}

// NewQuotaEvidenceReader builds the default fail-closed evidence reader.
func NewQuotaEvidenceReader() *QuotaEvidenceReader {
	return &QuotaEvidenceReader{usage: map[string]protection.QuotaUsage{}}
}

func evidenceKey(capability, principal string) string { return capability + "\x00" + principal }

// Report records observed usage for a (capability, principal) pair. Called by
// the telemetry path when wired; it is the ONLY way a definition becomes
// admissible (a definition with no evidence stays Unknown ⇒ fail-closed).
func (r *QuotaEvidenceReader) Report(capability, principal string, u protection.QuotaUsage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage[evidenceKey(capability, principal)] = u
}

// CurrentUsage implements protection.QuotaEvidenceReader. A previously reported
// observation is returned verbatim; anything else is Unknown (Complete=false) ⇒
// the Gate treats it as fail-closed per R23-4.
func (r *QuotaEvidenceReader) CurrentUsage(capability, principal string) (protection.QuotaUsage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if u, ok := r.usage[evidenceKey(capability, principal)]; ok {
		return u, nil
	}
	return protection.QuotaUsage{Complete: false}, nil
}
