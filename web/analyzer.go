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

	"go.uber.org/nilaway/accumulation"
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
	Requires: []*analysis.Analyzer{config.Analyzer, assertion.Analyzer, accumulation.Analyzer},
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

	// Get diagnostics (fired triggers) from accumulation.
	diagnostics := pass.ResultOf[accumulation.Analyzer].([]analysis.Diagnostic)

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

		// Producer is only annotated when its expression is authentic (not
		// an artificial node created by NilAway internally).
		var pStart, pEnd token.Position
		hasProducer := t.Producer.Expr != nil && pass.ExprIsAuthentic(t.Producer.Expr)
		if hasProducer {
			pStart = pass.Fset.Position(t.Producer.Expr.Pos())
			pEnd = pass.Fset.Position(t.Producer.Expr.End())
		}

		producerDesc, consumerDesc := t.Prestrings(pass)

		// Build a TriggerEntry; producer file/line may be empty when not authentic.
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
		cSrc, _ := pass.ReadFile(cStart.Filename)
		GlobalRegistry.addSpan(cStart.Filename, cSrc, &SpanData{
			Start:      cStart.Offset,
			End:        cEnd.Offset,
			IsProducer: false,
			TriggerIdx: triggerIdx,
			IsError:    isError,
			Tooltip:    consumerDesc.String(),
		})

		// Annotate the producer file (only if authentic and different from consumer file).
		if hasProducer {
			pSrc, _ := pass.ReadFile(pStart.Filename)
			GlobalRegistry.addSpan(pStart.Filename, pSrc, &SpanData{
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
