// Command opscore-pkg is the offline packaging tool (Phase 7.3). It reads a
// build spec and produces a verified package directory (dist/) containing the
// built helper(s), plugin.json, and release.json. It never touches the Runtime
// Core (Manifest / Provider / Loader / Manager / Compatibility / Execution stay
// frozen); the host later bridges the package via the single legal entry
// isolation.AddFromPackage.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/YuDong999/opscore/ecosystem/tooling"
)

func main() {
	specFile := ""
	outDir := "dist"
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--spec":
			if i+1 < len(os.Args) {
				specFile = os.Args[i+1]
				i++
			}
		case "--out":
			if i+1 < len(os.Args) {
				outDir = os.Args[i+1]
				i++
			}
		case "-h", "--help":
			usage()
			return
		}
	}
	if specFile == "" {
		fmt.Fprintln(os.Stderr, "error: --spec <build-spec.json> is required")
		usage()
		os.Exit(2)
	}

	raw, err := os.ReadFile(specFile)
	if err != nil {
		fatal(err)
	}
	var spec tooling.BuildSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fatal(fmt.Errorf("parse build spec %q: %w", specFile, err))
	}
	spec.OutDir = outDir

	if err := tooling.Build(context.Background(), spec, tooling.DefaultGoBuild()); err != nil {
		fatal(err)
	}
	fmt.Printf("built package %q v%s -> %s\n", spec.Name, spec.Version, outDir)
}

func usage() {
	fmt.Println("usage: opscore-pkg --spec <build-spec.json> [--out dist]")
	fmt.Println("  builds helper(s) via `go build`, writes plugin.json + release.json,")
	fmt.Println("  then verifies the produced package loads and validates.")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "opscore-pkg:", err)
	os.Exit(1)
}
