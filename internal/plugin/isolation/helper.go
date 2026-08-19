package isolation

import (
	"context"
	"encoding/json"
	"io"

	"github.com/YuDong999/opscore/ecosystem/sdk"
	"github.com/YuDong999/opscore/internal/core"
	"github.com/YuDong999/opscore/internal/core/snapshot"
)

// Serve is the HOST-SIDE helper half of the protocol: it adapts a set of
// in-repo core.Handler implementations to the SDK's standalone wire contract, so
// the very same plugin code that runs in-process can also run in a helper
// process (or, for third parties, be replaced by an SDK-built binary) without
// any change to the plugin. Internally it delegates to sdk.Serve, translating
// between core.Context / core.ExecutionPlan and the SDK's wire types.
//
// Serve returns an error only for transport failures. A plugin-level failure
// (unknown operation, handler error, unserializable plan) is reported inside
// the Response, so the host can distinguish "the plugin said no" from "the
// channel broke" — they demand different operator responses.
//
// Serve deliberately does not recover panics into a Response (see sdk.Serve):
// a panicking plugin takes its own process down, which the host reaps.
func Serve(r io.Reader, w io.Writer, handlers map[string]core.Handler) error {
	bridged := make(map[string]sdk.HandlerFunc, len(handlers))
	for op, h := range handlers {
		op, h := op, h
		bridged[op] = func(p sdk.ContextProjection, input map[string]any) (*sdk.PlanWire, error) {
			ctx := RebuildContext(context.Background(), p)
			plan, err := h.Plan(ctx, input)
			if err != nil {
				return nil, err
			}
			return EncodePlan(plan)
		}
	}
	return sdk.Serve(r, w, bridged)
}

// RebuildContext reconstructs a core.Context inside the helper from the
// projected wire context.
//
// Projection, never detection. If the host projected a CapabilitySnapshot and/
// or HostSnapshot, they are attached read-only — the helper sees the host's
// observed reality, including for a remote target, without ever probing its own
// machine (which would be the HELPER's capabilities, silently wrong for a
// remote target). If the host projected nothing, the helper stays
// Capability-blind and auto-detection remains disabled — the honest default.
// The helper NEVER calls DetectCapability; a capability set is only ever
// supplied by the host.
func RebuildContext(parent context.Context, p sdk.ContextProjection) core.Context {
	b := core.NewContext().
		WithStdContext(parent).
		WithUser(core.UserContext{ID: p.UserID, Name: p.UserName, Role: p.UserRole}).
		WithHost(core.HostContext{Hostname: p.Hostname, OS: p.OS, Arch: p.Arch}).
		WithTarget(core.TargetHost{
			Address: p.TargetAddress,
			Port:    p.TargetPort,
			User:    p.TargetUser,
			// No credentials: the host never sent any. See SDK ContextProjection.
		}).
		WithTraceID(p.TraceID).
		WithExecutionID(p.RequestID)

	if len(p.CapabilitySnapshot) > 0 {
		// Host-observed, read-only. Derives the decisioning surface and disables
		// auto-detect.
		var cs snapshot.CapabilitySnapshot
		if json.Unmarshal(p.CapabilitySnapshot, &cs) == nil {
			b = b.WithCapabilitySnapshot(&cs)
		}
	} else {
		// Capability-blind AND auto-detect disabled (empty ctx would otherwise
		// auto-detect the helper's own machine).
		b = b.WithCapability(core.CapabilityContext{})
	}
	if len(p.HostSnapshot) > 0 {
		var hs snapshot.HostSnapshot
		if json.Unmarshal(p.HostSnapshot, &hs) == nil {
			b = b.WithHostSnapshot(&hs)
		}
	}
	return b.Build()
}
