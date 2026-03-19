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

package web

import (
	"go/token"
	"os"

	"go.uber.org/nilaway"
	"go.uber.org/nilaway/annotation"
	"go.uber.org/nilaway/assertion"
	"go.uber.org/nilaway/config"
	"go.uber.org/nilaway/util/analysishelper"
	"golang.org/x/tools/go/analysis"
)

// Analyzer collects NilAway trigger data into GlobalRegistry.
// It must be run as a top-level analyzer (e.g. via checker.Analyze).
// It has no result type; all output goes into GlobalRegistry.
var Analyzer = &analysis.Analyzer{
	Name:     "nilaway_web",
	Doc:      "Collect NilAway trigger data for static web UI generation",
	Run:      run,
	Requires: []*analysis.Analyzer{config.Analyzer, assertion.Analyzer, nilaway.Analyzer},
}

func run(p *analysis.Pass) (interface{}, error) {
	pass := analysishelper.NewEnhancedPass(p)
	conf := pass.ResultOf[config.Analyzer].(*config.Config)

	if !conf.IsPkgInScope(pass.Pkg) {
		return nil, nil
	}

	// Get all triggers from the assertion analyzer.
	assertResult := pass.ResultOf[assertion.Analyzer].(*analysishelper.Result[[]annotation.FullTrigger])
	if assertResult.Err != nil {
		return nil, nil
	}
	triggers := assertResult.Res

	// Reuse the diagnostics already computed by nilaway.Analyzer instead of
	// re-querying accumulation.Analyzer independently.
	diagnostics := pass.ResultOf[nilaway.Analyzer].([]analysis.Diagnostic)

	// Build set of consumer positions that correspond to errors.
	errorPositions := make(map[token.Pos]bool, len(diagnostics))
	for _, d := range diagnostics {
		errorPositions[d.Pos] = true
	}

	GlobalRegistry.mu.Lock()
	defer GlobalRegistry.mu.Unlock()

	for _, t := range triggers {
		if t.CreatedFromDuplication || t.Consumer == nil || t.Consumer.Expr == nil {
			continue
		}

		isError := errorPositions[t.Consumer.Pos()]

		// Consumer is always available.
		cStart := pass.Fset.Position(t.Consumer.Expr.Pos())
		cEnd := pass.Fset.Position(t.Consumer.Expr.End())

		// For diagnostic reporting ExprIsAuthentic guards against misleading locations
		// from synthetic AST nodes. For the web report we relax this: synthetic nodes
		// created for cross-package producers (including stdlib) still carry obj.Pos(),
		// which is a real byte offset in the declaring file. We accept any producer
		// whose resolved position has a non-empty filename.
		var pStart, pEnd token.Position
		var hasProducer bool
		if t.Producer.Expr != nil {
			pos := pass.Fset.Position(t.Producer.Expr.Pos())
			if pos.IsValid() && pos.Filename != "" {
				pStart = pos
				pEnd = pass.Fset.Position(t.Producer.Expr.End())
				hasProducer = true
			}
		}

		producerDesc, consumerDesc := t.Prestrings(pass)

		// Build a TriggerEntry; producer file/line may be empty when position is unknown.
		entry := &TriggerEntry{
			ConsumerFile: cStart.Filename,
			ConsumerLine: cStart.Line,
			ConsumerDesc: consumerDesc.String(),
			IsError:      isError,
			ProducerDesc: producerDesc.String(),
		}
		if hasProducer {
			entry.ProducerFile = pStart.Filename
			entry.ProducerLine = pStart.Line
		}
		triggerIdx := GlobalRegistry.addTrigger(entry)

		// Annotate the consumer file.
		GlobalRegistry.addSpan(cStart.Filename, readSource(pass, cStart.Filename), &SpanData{
			Start:      cStart.Offset,
			End:        cEnd.Offset,
			IsProducer: false,
			TriggerIdx: triggerIdx,
			IsError:    isError,
			Tooltip:    consumerDesc.String(),
		})

		// Annotate the producer file. pass.ReadFile is restricted to the current
		// package, so fall back to os.ReadFile for cross-package files (stdlib etc.).
		if hasProducer {
			GlobalRegistry.addSpan(pStart.Filename, readSource(pass, pStart.Filename), &SpanData{
				Start:      pStart.Offset,
				End:        pEnd.Offset,
				IsProducer: true,
				TriggerIdx: triggerIdx,
				IsError:    isError,
				Tooltip:    producerDesc.String(),
			})
		}
	}

	return nil, nil
}

// readSource reads a file's contents using pass.ReadFile, falling back to os.ReadFile
// for files outside the current package (e.g. stdlib or other dependencies).
func readSource(pass *analysishelper.EnhancedPass, filename string) []byte {
	src, err := pass.ReadFile(filename)
	if err != nil || len(src) == 0 {
		src, _ = os.ReadFile(filename)
	}
	return src
}
