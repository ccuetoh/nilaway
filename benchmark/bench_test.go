//  Copyright (c) 2026 Uber Technologies, Inc.
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

// Explicitly enable proper types.Alias type for type aliases to ensure the benchmark uses the same behavior as tests.
//go:debug gotypesalias=1

package benchmark

import (
	"testing"

	"go.uber.org/nilaway"
	"go.uber.org/nilaway/config"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

func BenchmarkNilAway(b *testing.B) {
	// The following selection of packages was chosen to cover a variety of scenarios while keeping benchmark time
	// reasonable. It includes generics, error-heavy packages, complex times, and so on. Add new packages with care
	// for the impact on CI time.
	for _, pattern := range []string{
		"strconv",
		"context",
		"encoding/json",
		"go/types",
		"reflect",
		"regexp",
		"sync",
	} {
		b.Run(pattern, func(b *testing.B) {
			benchmarkPackage(b, pattern)
		})
	}
}

func benchmarkPackage(b *testing.B, pattern string) {
	b.Helper()

	setFlag(b, config.PrettyPrintFlag, "false")
	setFlag(b, config.GroupErrorMessagesFlag, "false")
	setFlag(b, config.IncludePkgsFlag, pattern)

	packagesToAnalyze, err := packages.Load(
		&packages.Config{
			Mode:  packages.LoadAllSyntax | packages.NeedModule,
			Tests: false,
		},
		pattern,
	)
	if err != nil {
		b.Fatal(err)
	}

	if len(packagesToAnalyze) == 0 {
		b.Fatalf("no packages loaded for %q", pattern)
	}

	checkPackageErrors(packagesToAnalyze, b)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		graph, err := checker.Analyze(
			[]*analysis.Analyzer{
				nilaway.Analyzer,
			},
			packagesToAnalyze,
			nil,
		)
		if err != nil {
			b.Fatal(err)
		}

		for action := range graph.All() {
			if action.Err != nil {
				b.Fatalf("analysis of %s with %s failed: %v", action.Package.PkgPath, action.Analyzer.Name, action.Err)
			}
		}
	}
}

func setFlag(b *testing.B, name, value string) {
	b.Helper()

	if err := config.Analyzer.Flags.Set(name, value); err != nil {
		b.Fatalf("set %s: %v", name, err)
	}
}

func checkPackageErrors(pkgs []*packages.Package, b *testing.B) {
	b.Helper()

	var (
		seen  = make(map[*packages.Package]struct{})
		check func(*packages.Package)
	)

	check = func(pkg *packages.Package) {
		if _, contains := seen[pkg]; contains {
			return
		}

		seen[pkg] = struct{}{}
		for _, err := range pkg.Errors {
			b.Fatalf("loading %s: %v", pkg.PkgPath, err)
		}
		for _, imported := range pkg.Imports {
			check(imported)
		}
	}

	for _, pkg := range pkgs {
		check(pkg)
	}
}
