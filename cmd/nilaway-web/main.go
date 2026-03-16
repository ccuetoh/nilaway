// Copyright (c) 2025 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// main package provides the nilaway-web binary, which runs NilAway analysis and
// generates a static HTML report visualizing nil-flow triggers (both errors and
// safely-handled cases) with annotated source code.
//
// Usage:
//
//	nilaway-web [flags] [packages]
//
// Flags:
//
//	-output-dir <dir>   directory for the generated HTML site (default: nilaway-web)
//	-include-pkgs <...> comma-separated package prefixes to analyze (passed to NilAway)
//	(all other NilAway config flags are also accepted)
//
// Example:
//
//	nilaway-web -include-pkgs="github.com/myorg/myproject" -output-dir=report ./...
//	open report/index.html
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"go.uber.org/nilaway/config"
	nilawayWeb "go.uber.org/nilaway/web"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

func main() {
	// Lift NilAway config flags to the top level (same pattern as cmd/nilaway/main.go)
	// so users can write -include-pkgs=... instead of -nilaway_config.include-pkgs=...
	config.Analyzer.Flags.VisitAll(func(f *flag.Flag) {
		flag.Var(f.Value, f.Name, f.Usage)
	})

	var outputDir string
	flag.StringVar(&outputDir, "output-dir", "nilaway-web", "directory for the generated HTML site")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: nilaway-web [flags] [packages]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	// Load packages with full syntax information required by go/analysis.
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo |
			packages.NeedTypesSizes,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		log.Fatalf("load packages: %v", err)
	}

	// Check for package loading errors.
	var loadErrors int
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			fmt.Fprintf(os.Stderr, "%v\n", e)
			loadErrors++
		}
	}
	if loadErrors > 0 {
		log.Fatalf("encountered %d package loading error(s)", loadErrors)
	}

	// Run NilAway analysis. web.Analyzer transitively requires assertion.Analyzer
	// and accumulation.Analyzer (and their sub-analyzers), so a single top-level
	// analyzer is sufficient.
	_, err = checker.Analyze([]*analysis.Analyzer{nilawayWeb.Analyzer}, pkgs, nil)
	if err != nil {
		log.Fatalf("analysis: %v", err)
	}

	// Generate the static HTML site from the collected registry data.
	if err := nilawayWeb.Generate(outputDir, nilawayWeb.GlobalRegistry); err != nil {
		log.Fatalf("generate HTML: %v", err)
	}

	log.Printf("NilAway web report written to %s/index.html", outputDir)
}
