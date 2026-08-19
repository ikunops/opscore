// Package snapshot defines the OBSERVATION surface of Capability Discovery —
// the serializable, cacheable, remotely-transmittable description of "what a
// host reports it can do" and "who a host is".
//
// This is deliberately separate from core.CapabilityContext (the live,
// boolean decisioning surface that Handlers branch on at plan time). See
// ADR-009 (Capability Snapshot Separation): the decisioning surface answers
// "what should I do?"; this snapshot answers "what does this host report?".
//
// Design rules for this package:
//   - Pure data + small helpers only. NO os/exec, NO network, NO import of
//     core or the capability probe package (that would create an import
//     cycle: core -> snapshot -> capability -> core). Probes live in the
//     capability package (local) and in core/ssh.go (remote).
//   - A snapshot is a value type: captured at a point in time and frozen. It is
//     never re-derived lazily from a live host after the fact (audit integrity).
//   - SnapshotSource lets Local / SSH / Cache probes reuse the exact same
//     structures, so the Resolver, ExecutionRecord, and UI never care where
//     the data came from — they only consume a uniform Snapshot.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// SnapshotSource identifies where a snapshot was collected from. Consumers
// (Resolver, UI, audit) do not branch on this; it exists for observability and
// debugging, and to keep Local/SSH/Cache paths unified under one type.
type SnapshotSource string

const (
	SourceLocal SnapshotSource = "local" // collected in-process on the control plane host
	SourceSSH   SnapshotSource = "ssh"   // collected over SSH from a remote target
	SourceCache SnapshotSource = "cache" // served from a previous collection (host registry hint)
)

// CapabilityInfo is a single discovered capability of a host. It is
// intentionally richer than the boolean CapabilityContext used by Handlers for
// fast decisioning: it carries a version and free-form details.
type CapabilityInfo struct {
	Name      string            `json:"name"`
	Available bool              `json:"available"`
	Version   string            `json:"version,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// CapabilitySnapshot is the frozen, observed set of a host's capabilities.
// Items is keyed by capability name for O(1) Resolver lookups
// (e.g. caps.Items["systemd"]), avoiding linear scans on the hot path.
type CapabilitySnapshot struct {
	HostID      string                    `json:"host_id"`
	Version     int64                     `json:"version"`
	Items       map[string]CapabilityInfo `json:"items"`
	CollectedAt time.Time                 `json:"collected_at"`
	Source      SnapshotSource            `json:"source"`
}

// Available reports whether the named capability was discovered. A missing key
// is treated as not-available (never panics).
func (s *CapabilitySnapshot) Available(name string) bool {
	if s == nil || s.Items == nil {
		return false
	}
	return s.Items[name].Available
}

// Get returns the CapabilityInfo for name and whether it was present.
func (s *CapabilitySnapshot) Get(name string) (CapabilityInfo, bool) {
	if s == nil || s.Items == nil {
		return CapabilityInfo{}, false
	}
	info, ok := s.Items[name]
	return info, ok
}

// Hash returns a stable, content-derived fingerprint of the capability set.
// Used by ExecutionRecord (ADR-009 CapabilityHash) to detect capability drift
// across executions without storing the full snapshot inline.
func (s *CapabilitySnapshot) Hash() string {
	if s == nil {
		return ""
	}
	keys := make([]string, 0, len(s.Items))
	for k := range s.Items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		info := s.Items[k]
		h.Write([]byte(k))
		h.Write([]byte(info.Name))
		if info.Available {
			h.Write([]byte("1"))
		} else {
			h.Write([]byte("0"))
		}
		h.Write([]byte(info.Version))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Sorted returns the capabilities in deterministic (name-sorted) order. Used
// when surfacing the snapshot to a UI/API so diffs are stable.
func (s *CapabilitySnapshot) Sorted() []CapabilityInfo {
	if s == nil || s.Items == nil {
		return nil
	}
	out := make([]CapabilityInfo, 0, len(s.Items))
	for _, v := range s.Items {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HostSnapshot is the static identity of a host — "who am I?", deliberately
// separate from "what can I do?" (CapabilitySnapshot). It is serializable and
// cacheable so the control plane can remember a host across connections.
type HostSnapshot struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Address     string         `json:"address"`
	OS          string         `json:"os"`                 // linux | windows | darwin
	Arch        string         `json:"arch"`               // amd64 | arm64
	Platform    string         `json:"platform,omitempty"` // ubuntu/debian/rhel/alpine...
	Version     string         `json:"version,omitempty"`  // distro version
	Kernel      string         `json:"kernel,omitempty"`   // uname -r
	User        string         `json:"user,omitempty"`     // effective SSH user
	CollectedAt time.Time      `json:"collected_at"`
	Source      SnapshotSource `json:"source"`
}
