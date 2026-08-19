// Command opscore-cli is the thin Phase 11.1 CLI. It binds ONLY to the external/v1 contract exposed
// by internal/external. It reaches data exclusively through external.Server, which reads via
// internal/platformview and internal/correlation — the sanctioned read sources (ADR-024 §1, R56
// mechanical guard: "platformview / correlation 之外不得建立新的读源"). The CLI never imports the
// frozen internals (runtime / plugin / isolation / controlplane), so it cannot bypass the External
// Contract.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/YuDong999/opscore/internal/correlation"
	"github.com/YuDong999/opscore/internal/external"
	"github.com/YuDong999/opscore/internal/platformview"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run dispatches subcommands, each returning an external/v1 DTO.
func run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: opscore-cli <execution|host|policy|correlation> [args]")
	}

	// Build the External Interface Server. Production would inject real Readers wired to the
	// capabilities; here the facades are constructed with nil Readers (the facades tolerate nil and
	// return views with empty sub-fields). The CLI only ever touches external.Server — it never
	// reaches past the facades into the frozen systems (ADR-024 MUST-0/1).
	pv := platformview.New(platformview.Readers{})
	corr := correlation.New(correlation.Readers{})
	srv := external.NewServer(pv, corr, nil)

	switch args[0] {
	case "execution":
		if len(args) < 2 {
			return fmt.Errorf("usage: opscore-cli execution <executionID>")
		}
		return printOrErr(srv.GetExecution(ctx, args[1]))
	case "host":
		if len(args) < 2 {
			return fmt.Errorf("usage: opscore-cli host <hostRef>")
		}
		return printOrErr(srv.GetHost(ctx, args[1]))
	case "policy":
		if len(args) < 2 {
			return fmt.Errorf("usage: opscore-cli policy <policyID>")
		}
		return printOrErr(srv.GetPolicy(ctx, args[1]))
	case "correlation":
		if len(args) < 3 {
			return fmt.Errorf("usage: opscore-cli correlation <kind> <ref>")
		}
		return printOrErr(srv.GetCorrelation(ctx, external.ScopeDTO{Kind: args[1], Ref: args[2]}))
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printOrErr(v interface{}, err error) error {
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
