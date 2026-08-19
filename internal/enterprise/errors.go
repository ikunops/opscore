package enterprise

import (
	"errors"
	"fmt"
)

// Sentinel errors — all are policy-layer metadata errors, never execution
// failures (ADR-017 MUST-1: Enterprise never produces execution errors).
var (
	// ErrInvalidTarget: an empty target reference was supplied to Attach.
	ErrInvalidTarget = errors.New("enterprise: target ref must not be empty")
	// ErrInvalidPolicy: an empty policy kind was supplied to Attach.
	ErrInvalidPolicy = errors.New("enterprise: policy kind must not be empty")
	// ErrAttachmentNotFound: Detach was called for an unknown AttachID.
	ErrAttachmentNotFound = errors.New("enterprise: attachment not found")
)

// newAttachID mints an opaque, enterprise-local handle for an attachment. It
// is derived from a monotonic sequence — it is NOT a new identity system and
// never replaces the TargetRef an attachment is bound to.
func newAttachID(seq uint64) string {
	return fmt.Sprintf("ent-%d", seq)
}
