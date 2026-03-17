// Copyright (c) 2023 Uber Technologies, Inc.
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

// main package makes it possible to build NilAway as a standalone code checker that can be
// independently invoked to check other packages. It also makes it possible to run cpu and mem
// profiles on NilAway through command line arguments when analyzing packages.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/nilaway"
	"go.uber.org/nilaway/config"
	"go.uber.org/nilaway/util/analysishelper"
	nilawayWeb "go.uber.org/nilaway/web"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/analysis/singlechecker"
	"golang.org/x/tools/go/packages"
)

// Analyzer is identical to the one in nilaway.go, except that it overrides the run function for
// extra filtering of errors, since the singlechecker does not support error suppression like other
// popular linter drivers.
var Analyzer = &analysis.Analyzer{
	Name:       nilaway.Analyzer.Name,
	Doc:        nilaway.Analyzer.Doc,
	Run:        run,
	FactTypes:  nilaway.Analyzer.FactTypes,
	ResultType: nilaway.Analyzer.ResultType,
	Requires:   nilaway.Analyzer.Requires,
}

var (
	// _includeErrorsInFiles is a driver flag for specifying the list of file prefixes to only report errors.
	_includeErrorsInFiles string
	// _excludeErrorsInFiles is a driver flag for specifying the list of file prefixes to not report errors.
	_excludeErrorsInFiles string
)

func run(p *analysis.Pass) (interface{}, error) {
	pass := analysishelper.NewEnhancedPass(p)
	// NilAway by default analyzes all packages, including dependencies. Even if specified to
	// exclude packages from analysis via configurations, NilAway can still report errors on
	// packages that are not analyzed if the nilness flow happens within the analyzed package, but
	// the flow concerns a struct that is in an excluded package. The usual way to handle them is
	// to suppress them at the driver level, but singlechecker does not support that yet. Therefore,
	// here we add extra logic to filter the errors.

	// Properly parse the error suppression flags.
	includes, err := parseFilePrefixes(_includeErrorsInFiles)
	if err != nil {
		return nil, fmt.Errorf("parse file prefixes for error inclusion: %w", err)
	}
	excludes, err := parseFilePrefixes(_excludeErrorsInFiles)
	if err != nil {
		return nil, fmt.Errorf("parse file prefixes for error exclusion: %w", err)
	}

	// Override the report function to add error filtering logic.
	report := pass.Report
	pass.Report = func(d analysis.Diagnostic) {
		p := pass.Fset.File(d.Pos).Name()
		for _, e := range excludes {
			if strings.HasPrefix(p, e) {
				return
			}
		}

		for _, i := range includes {
			if strings.HasPrefix(p, i) {
				report(d)
				return
			}
		}
	}

	// Delegate the real analysis run to the original nilaway analyzer.
	return nilaway.Analyzer.Run(p)
}

// parseFilePrefixes parses the comma-separated list of file prefixes, converts them to absolute
// file paths, and returns them as a slice.
func parseFilePrefixes(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}

	// Convert the file paths to absolute paths.
	list := strings.Split(s, ",")
	for i := range list {
		p, err := filepath.Abs(list[i])
		if err != nil {
			return nil, fmt.Errorf("convert %q to absolute path: %w", list[i], err)
		}
		list[i] = p
	}
	return list, nil
}

func main() {
	// For better UX, we lift the flags from config.Analyzer to the top level so that users can
	// specify them without having to specify the analyzer name ("nilaway_config").
	// For example, without lifting the flags, we will have to use `multichecker` to run the
	// top-level NilAway analyzer _and_ the config analyzer. Users will have to specify flags as
	// the following (directed to the "nilaway_config" analyzer):
	//
	// `nilaway -nilaway_config.flag1 <VALUE1> -nilaway_config.flag2 <VALUE> ./...`
	//
	// With this, the flags will be exposed at the top level, making "nilaway_config" analyzer
	// transparent to the users:
	//
	// `nilaway -flag1 <VALUE1> -flag2 <VALUE> ./...`
	//
	config.Analyzer.Flags.VisitAll(func(f *flag.Flag) { flag.Var(f.Value, f.Name, f.Usage) })

	// Add two more flags to the driver for error suppression since singlechecker does not support it.
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get working directory: %v\n", err)
		os.Exit(1)
	}
	flag.StringVar(&_includeErrorsInFiles, "include-errors-in-files", wd, "A comma-separated list of file prefixes to report errors, default is current working directory.")
	flag.StringVar(&_excludeErrorsInFiles, "exclude-errors-in-files", "", "A comma-separated list of file prefixes to exclude from error reporting. This takes precedence over include-errors-in-files.")

	// When -output-web is set, the binary generates a static HTML trigger visualization
	// in addition to normal error reporting. We use the programmatic checker.Analyze path
	// in this case since singlechecker calls os.Exit and does not allow post-analysis work.
	var outputWebDir string
	flag.StringVar(&outputWebDir, "output-web", "", "If non-empty, generate a static HTML trigger visualization report in this directory.")

	// Parse flags early so we can check -output-web before deciding which execution path to take.
	// singlechecker.Main will call flag.Parse again, but that is idempotent.
	flag.Parse()

	if outputWebDir != "" {
		os.Exit(runWebMode(flag.Args(), outputWebDir))
	}

	singlechecker.Main(Analyzer)
}

// runWebMode runs the analysis programmatically (bypassing singlechecker) so that we can
// generate the HTML report after all packages have been analyzed.
// It prints diagnostics to stderr in the same format as singlechecker and returns an exit code.
func runWebMode(patterns []string, outputDir string) int {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

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
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load packages: %v\n", err)
		return 1
	}

	var loadErrors bool
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			fmt.Fprintln(os.Stderr, e)
			loadErrors = true
		}
	}
	if loadErrors {
		return 1
	}

	// Run both Analyzer (for diagnostics) and web.Analyzer (for the HTML report).
	// The checker deduplicates shared sub-analyzers so the analysis work is not doubled.
	graph, err := checker.Analyze(
		[]*analysis.Analyzer{Analyzer, nilawayWeb.Analyzer},
		pkgs, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analysis: %v\n", err)
		return 1
	}

	// Print diagnostics in the same format as singlechecker.
	exitCode := 0
	for act := range graph.All() {
		if act.Analyzer != Analyzer {
			continue
		}
		for _, d := range act.Diagnostics {
			posn := act.Package.Fset.Position(d.Pos)
			fmt.Fprintf(os.Stderr, "%s: %s\n", posn, d.Message)
			exitCode = 1
		}
	}

	if err := nilawayWeb.Generate(outputDir, nilawayWeb.GlobalRegistry); err != nil {
		fmt.Fprintf(os.Stderr, "generate web report: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "NilAway web report: %s/index.html\n", outputDir)
	return exitCode
}
