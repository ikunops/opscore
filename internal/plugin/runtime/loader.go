package runtime

import (
	"context"
	"fmt"
)

// Loader abstracts WHERE a plugin comes from. It deliberately never deals in
// file paths or .so handles — a future FileLoader / OCIRegistryLoader /
// GitLoader all implement THIS interface (GPT Round 6/7: no Load(path),
// no LoadFile, anti-.so slide). The input is an abstract plugin SOURCE, not
// a filesystem path.
type Loader interface {
	// Discover returns the Descriptors the loader can see. Each is in state
	// Discovered (not yet loaded).
	Discover(ctx context.Context) []Descriptor
	// Load resolves a Discovered descriptor into a running Module (state Loaded).
	Load(desc Descriptor) (Module, error)
	// Unload tears down a previously loaded Module by plugin name.
	Unload(name string) error
}

// StaticLoader is a contract/test Loader: it returns a fixed set of
// descriptors and maps each to a pre-built Module.
type StaticLoader struct {
	descs []Descriptor
	mods  map[string]Module
}

// NewStaticLoader builds a Loader from a name->Module map.
func NewStaticLoader(mods map[string]Module) *StaticLoader {
	descs := make([]Descriptor, 0, len(mods))
	mm := make(map[string]Module, len(mods))
	for name, m := range mods {
		// The module's own descriptor is Loaded+Frozen. For the
		// discovered set we expose a Discovered, un-frozen COPY (value
		// semantics) so Discover() reads as "seen but not yet loaded".
		d := m.Descriptor()
		d.State = StateDiscovered
		d.frozen = false
		descs = append(descs, d)
		mm[name] = m
	}
	return &StaticLoader{descs: descs, mods: mm}
}

func (l *StaticLoader) Discover(ctx context.Context) []Descriptor {
	out := make([]Descriptor, len(l.descs))
	copy(out, l.descs)
	return out
}

func (l *StaticLoader) Load(desc Descriptor) (Module, error) {
	name := desc.Manifest.Name
	m, ok := l.mods[name]
	if !ok {
		return nil, fmt.Errorf("static loader: no module for plugin %q", name)
	}
	// The module's descriptor is the authoritative Loaded+Frozen copy.
	md := m.Descriptor()
	if !md.IsFrozen() {
		md.Freeze()
	}
	return m, nil
}

func (l *StaticLoader) Unload(name string) error {
	if _, ok := l.mods[name]; !ok {
		return fmt.Errorf("static loader: unknown plugin %q", name)
	}
	delete(l.mods, name)
	return nil
}
