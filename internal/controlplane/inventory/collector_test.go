package inventory

import (
	"errors"
	"testing"

	"github.com/YuDong999/opscore/internal/core"
)

func TestCollect_AggregatesSkipsAndFails(t *testing.T) {
	calls := map[string]int{}
	runner := func(ctx core.Context, opName string) ([]string, error) {
		calls[opName]++
		switch opName {
		case "system.disk.mounts":
			return []string{"mount-a", "mount-b"}, nil
		case "system.host.info":
			return []string{"uptime 1"}, nil
		case "system.user.list":
			return nil, ErrOpUnauthorized
		case "system.package.list":
			return nil, errors.New("dpkg locked")
		}
		return nil, errors.New("unknown op: " + opName)
	}

	ctx := core.NewContext().Build()
	d := Collect(ctx, runner)

	if len(d.Ops) != len(ReadOnlyWhitelist) {
		t.Fatalf("expected %d ops, got %d", len(ReadOnlyWhitelist), len(d.Ops))
	}
	byName := map[string]OpResult{}
	for _, o := range d.Ops {
		byName[o.Name] = o
	}

	// Aggregated output preserved in order.
	mounts := byName["system.disk.mounts"]
	if !mounts.Ok || len(mounts.Steps) != 2 || mounts.Steps[0] != "mount-a" {
		t.Fatalf("mounts section wrong: %+v", mounts)
	}

	// Unauthorized -> Skipped, not failed.
	users := byName["system.user.list"]
	if !users.Skipped || users.Ok {
		t.Fatalf("user.list should be skipped: %+v", users)
	}

	// Failure -> Ok=false with error captured, not a panic.
	pkgs := byName["system.package.list"]
	if pkgs.Ok || pkgs.Error == "" {
		t.Fatalf("package.list should record failure: %+v", pkgs)
	}

	// Every whitelisted op was actually attempted.
	for _, op := range ReadOnlyWhitelist {
		if calls[op.Name] == 0 {
			t.Fatalf("op %s was never run", op.Name)
		}
	}
}

func TestCollect_UnreachableTarget_StillBuilds(t *testing.T) {
	// A runner that fails every op must not make Collect panic or return nil.
	runner := func(ctx core.Context, opName string) ([]string, error) {
		return nil, errors.New("target down")
	}
	d := Collect(core.NewContext().Build(), runner)
	if d == nil || len(d.Ops) != len(ReadOnlyWhitelist) {
		t.Fatalf("collect must always return a full Detail, got %+v", d)
	}
	for _, o := range d.Ops {
		if o.Ok {
			t.Fatalf("op %s should not be ok when runner fails: %+v", o.Name, o)
		}
	}
}
