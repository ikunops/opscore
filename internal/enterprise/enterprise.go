package enterprise

import (
	"sort"
	"sync"
	"time"
)

// Service is the in-memory policy-attachment store. It holds ONLY policy
// metadata bound to existing IDs — it owns no execution state and exposes NO
// method that runs a command or produces a verdict (ADR-017 MUST-1/4).
//
// It is safe for concurrent use.
type Service struct {
	mu          sync.RWMutex
	attachments map[string]PolicyAttachment // keyed by AttachID
	seq         uint64
}

// NewService builds an empty enterprise policy service.
func NewService() *Service {
	return &Service{attachments: make(map[string]PolicyAttachment)}
}

// Attach binds a policy (kind) to an existing target (kind+ref) with the given
// metadata. It returns the created attachment. Attach is idempotent in spirit
// but always mints a new AttachID so multiple policies can stack on one target.
//
// Attach performs NO execution and NO evaluation — it records policy state only
// (ADR-017 MUST-1/3/4). The verdict that may result from this attachment is
// produced later by Governance (ADR-018), not here.
func (s *Service) Attach(target TargetKind, ref string, kind PolicyKind, meta map[string]string) (PolicyAttachment, error) {
	if ref == "" {
		return PolicyAttachment{}, ErrInvalidTarget
	}
	if kind == "" {
		return PolicyAttachment{}, ErrInvalidPolicy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	att := PolicyAttachment{
		AttachID:   newAttachID(s.seq),
		TargetKind: target,
		TargetRef:  ref,
		Kind:       kind,
		Metadata:   cloneMeta(meta),
		CreatedAt:  time.Now(),
	}
	s.attachments[att.AttachID] = att
	return att, nil
}

// Detach removes a policy attachment by its enterprise-local AttachID. Unknown
// IDs are a no-op (return nil) — detachment never errors on absence.
func (s *Service) Detach(attachID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.attachments[attachID]; !ok {
		return ErrAttachmentNotFound
	}
	delete(s.attachments, attachID)
	return nil
}

// AttachmentsFor returns all policy attachments bound to a given target
// (kind+ref), sorted by AttachID for stable output. Returns nil if the target
// has none.
func (s *Service) AttachmentsFor(target TargetKind, ref string) []PolicyAttachment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PolicyAttachment
	for _, att := range s.attachments {
		if att.TargetKind == target && att.TargetRef == ref {
			out = append(out, att)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AttachID < out[j].AttachID })
	return out
}

// All returns every policy attachment, sorted by AttachID. Used by the
// platform's org-policy surface. Read-only; it never mutates state.
func (s *Service) All() []PolicyAttachment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PolicyAttachment, 0, len(s.attachments))
	for _, att := range s.attachments {
		out = append(out, att)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AttachID < out[j].AttachID })
	return out
}

func cloneMeta(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
