// Package tracing provides causal tracing for OpsCore plugin executions.
//
// A trace is a passenger, not a driver: it observes executions without executing
// them (R20-4, ADR-043). Spans carry independent, random identities (R20-6) and
// travel through context only — no global state, no goroutine-local (R20-5).
// Span lifecycle is non-blocking (R20-7); End() never blocks on I/O.
//
// The TraceRing is bounded and honest (R20-8): eviction is counted via
// DroppedCount and surfaced as a truncated flag, never silently dropped (R20-10).
// Eligible spans are captured wholesale — there is no sampling (R20-9). The
// package depends only on the standard library (R20-2).
package tracing
