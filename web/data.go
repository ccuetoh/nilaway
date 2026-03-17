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

// Package web provides static HTML site generation for NilAway trigger visualization.
package web

import "sync"

// SpanData represents a highlighted region in a source file,
// corresponding to either a producer or a consumer site of a trigger.
type SpanData struct {
	// Start and End are byte offsets in the source file.
	Start      int
	End        int
	IsProducer bool // true = producer (nil source), false = consumer (nil use)
	TriggerIdx int  // index into Registry.Triggers
	IsError    bool // true if this trigger fires (would cause a nil panic)
	Tooltip    string
	// ID and Link are populated by Generate before rendering, not during analysis.
	ID   string // HTML id attribute for anchor targeting (e.g. "t5-prod")
	Link string // href to navigate to the counterpart span (may be empty)
}

// TriggerEntry holds the full producer→consumer relationship for one trigger.
type TriggerEntry struct {
	ProducerFile string
	ProducerLine int
	ProducerDesc string
	ConsumerFile string
	ConsumerLine int
	ConsumerDesc string
	IsError      bool
}

// FileData holds all annotation data for a single source file.
type FileData struct {
	Filename string
	Source   []byte
	Spans    []*SpanData
}

// Registry collects trigger data across all analyzed packages.
// It is populated concurrently by web.Analyzer during analysis.
type Registry struct {
	mu       sync.Mutex
	Files    map[string]*FileData // absolute filename → file data
	Triggers []*TriggerEntry
}

// GlobalRegistry is populated by web.Analyzer during analysis and
// consumed by Generate to write the static site.
var GlobalRegistry = &Registry{
	Files: make(map[string]*FileData),
}

// addTrigger appends a TriggerEntry and returns its index.
func (r *Registry) addTrigger(t *TriggerEntry) int {
	r.Triggers = append(r.Triggers, t)
	return len(r.Triggers) - 1
}

// addSpan records a highlighted span for the given file.
// If the file has not been seen before, source is stored.
func (r *Registry) addSpan(filename string, source []byte, span *SpanData) {
	fd, ok := r.Files[filename]
	if !ok {
		fd = &FileData{Filename: filename, Source: source}
		r.Files[filename] = fd
	}
	fd.Spans = append(fd.Spans, span)
}
