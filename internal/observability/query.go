package observability

// Query filters observations for the read API (Dashboard API). Every non-empty
// field must match; an empty field is a wildcard. This is the read-only
// surface — it never mutates the store (ADR-015 MUST-1).
type Query struct {
	TraceID     string
	ExecutionID string
	PluginID    string
	Source      Source
	Kind        Kind
}

// Query returns observations matching all non-empty fields of q, in ingest
// order (oldest first). It is safe for concurrent use.
//
// Scope (Phase 18, ADR-040 §3.3): the result is drawn from the RETAINED WINDOW
// — at most Capacity() observations. If Complete() is false, an empty result
// means "no match in the window", NOT "never happened"; the exact all-time
// answer for aggregate questions is Counter/Counters, which eviction cannot
// affect. The signature is deliberately unchanged so the frozen consumers in
// platformview/correlation see no shape change (ADR-040 §2).
func (c *Collector) Query(q Query) []Observation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []Observation
	for i := 0; i < len(c.obs); i++ {
		o := c.at(i)
		if q.TraceID != "" && o.TraceID != q.TraceID {
			continue
		}
		if q.ExecutionID != "" && o.ExecutionID != q.ExecutionID {
			continue
		}
		if q.PluginID != "" && o.PluginID != q.PluginID {
			continue
		}
		if q.Source != "" && o.Source != q.Source {
			continue
		}
		if q.Kind != "" && o.Kind != q.Kind {
			continue
		}
		out = append(out, o)
	}
	return out
}

// Counter returns the value of a named, label-keyed counter (0 if absent).
// Counters are bumped at ingest and never re-derived from the observation
// buffer, so they are EXACT for all time regardless of eviction (ADR-040 §3.3).
func (c *Collector) Counter(name string, labels map[string]string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.counters[counterKey(name, labels)]
}

// Counters returns a snapshot of all counters.
//
// The two opscore_observations_* keys are always present — even at zero — so
// the metrics surface can render an exact lifetime total and an exact drop
// count without special-casing them, and a genuine zero is expressed as "0",
// not omitted (which would make "never happened" indistinguishable from "not
// scraped"). R19-7: these are the EXACT aggregates, never the bounded window.
func (c *Collector) Counters() map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]int64, len(c.counters)+2)
	for k, v := range c.counters {
		out[k] = v
	}
	out[counterKey("opscore_observations_total", nil)] = c.total
	out[counterKey("opscore_observations_dropped", nil)] = c.dropped
	return out
}

// Count returns the number of observations in the RETAINED WINDOW — bounded by
// Capacity(), not the all-time ingest total. For an exact all-time figure use
// Counter("observations_total", …), which eviction does not touch.
func (c *Collector) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.obs)
}
