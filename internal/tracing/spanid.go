package tracing

import (
	"crypto/rand"
	"encoding/hex"
)

// newSpanID mints a span identity unique within its trace. Independent of any
// existing ID and of the trace id (R20-6).
func newSpanID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}
