package tracing

import (
	"crypto/rand"
	"encoding/hex"
)

// NewTraceID mints an opaque, random trace identity. It is NOT derived from any
// existing ID (R20-6): even given an ExecutionID, the TraceID is independent and
// must never be reconstructed from it (R20-10 advisory resolution). Two calls
// yield distinct ids (P-2). Exported so the observability adapter can mint
// independent trace identities at the ingestion boundary without re-implementing
// the random discipline.
func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is non-blocking on a healthy host; a failure here is
		// unrecoverable, so we return a zero id rather than panic (P-1 nil-safety).
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
