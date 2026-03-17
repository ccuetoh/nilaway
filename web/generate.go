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
	"bytes"
	"crypto/sha256"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// indexRow is used as template data for one row in the index page.
type indexRow struct {
	Filename string
	Link     string
	Errors   int
	Safe     int
	Total    int
}

// indexData is the template data for the index page.
type indexData struct {
	TotalFiles      int
	TotalErrors     int
	TotalSafe       int
	TotalTriggers   int
	StdlibTriggers  int // triggers in stdlib files, omitted from Rows
	Rows            []indexRow
}

// triggerRow is used as template data for one trigger in a file page.
type triggerRow struct {
	Idx          int
	IsError      bool
	ProducerFile string
	ProducerLine int
	ProducerLink string
	ConsumerFile string
	ConsumerLine int
	ConsumerLink string
	ProducerDesc string
	ConsumerDesc string
}

// filePageData is the template data for a file page.
type filePageData struct {
	Filename        string
	AnnotatedSource template.HTML
	Rows            []triggerRow
}

// Generate writes a self-contained static HTML site to outputDir.
// The index page lists all files with trigger counts; each file page shows
// annotated source code and a trigger table.
func Generate(outputDir string, registry *Registry) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Collect filenames in sorted order for deterministic output.
	filenames := make([]string, 0, len(registry.Files))
	for fn := range registry.Files {
		filenames = append(filenames, fn)
	}
	sort.Strings(filenames)

	var (
		rows           []indexRow
		totalErrors    int
		totalSafe      int
		stdlibTriggers int
	)

	for _, fn := range filenames {
		fd := registry.Files[fn]
		htmlFile := filePageName(fn)

		// Always generate the file page so cross-file trigger links resolve.
		if err := writeFilePage(outputDir, htmlFile, fd, registry); err != nil {
			return fmt.Errorf("write file page for %s: %w", fn, err)
		}

		errs, safe := countSpans(fd)
		// Stdlib files are kept out of the main index to reduce noise; their
		// pages still exist so producer/consumer links from user-code pages work.
		if isStdlibFile(fn) {
			stdlibTriggers += errs + safe
			continue
		}
		rows = append(rows, indexRow{
			Filename: fn,
			Link:     htmlFile,
			Errors:   errs,
			Safe:     safe,
			Total:    errs + safe,
		})
		totalErrors += errs
		totalSafe += safe
	}

	return writeIndexPage(outputDir, rows, totalErrors, totalSafe, stdlibTriggers)
}

// filePageName returns a safe HTML filename for an absolute source path.
func filePageName(absPath string) string {
	h := sha256.Sum256([]byte(absPath))
	base := strings.TrimSuffix(filepath.Base(absPath), ".go")
	return fmt.Sprintf("%s_%x.html", base, h[:4])
}

// countSpans counts unique error and safe triggers for a file.
func countSpans(fd *FileData) (errors, safe int) {
	seen := make(map[int]bool)
	for _, s := range fd.Spans {
		if seen[s.TriggerIdx] {
			continue
		}
		seen[s.TriggerIdx] = true
		if s.IsError {
			errors++
		} else {
			safe++
		}
	}
	return
}

// isStdlibFile reports whether filename lives inside the Go standard library.
func isStdlibFile(filename string) bool {
	root := runtime.GOROOT()
	return root != "" && strings.HasPrefix(filename, root+string(filepath.Separator))
}

// writeIndexPage writes index.html to outputDir.
func writeIndexPage(outputDir string, rows []indexRow, totalErrors, totalSafe, stdlibTriggers int) error {
	f, err := os.Create(filepath.Join(outputDir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	d := indexData{
		TotalFiles:     len(rows),
		TotalErrors:    totalErrors,
		TotalSafe:      totalSafe,
		TotalTriggers:  totalErrors + totalSafe,
		StdlibTriggers: stdlibTriggers,
		Rows:           rows,
	}
	return indexTmpl.Execute(f, d)
}

// writeFilePage generates the annotated source page for a single file.
func writeFilePage(outputDir, htmlFile string, fd *FileData, registry *Registry) error {
	f, err := os.Create(filepath.Join(outputDir, htmlFile))
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	// Populate ID and cross-link for each span so annotateSource can render
	// clickable <a> elements that navigate to the paired producer/consumer.
	for _, sp := range fd.Spans {
		t := registry.Triggers[sp.TriggerIdx]
		role, otherRole, otherFile := "cons", "prod", t.ProducerFile
		if sp.IsProducer {
			role, otherRole, otherFile = "prod", "cons", t.ConsumerFile
		}
		sp.ID = fmt.Sprintf("t%d-%s", sp.TriggerIdx, role)
		switch {
		case otherFile == "":
			sp.Link = ""
		case otherFile == fd.Filename:
			sp.Link = fmt.Sprintf("#t%d-%s", sp.TriggerIdx, otherRole)
		default:
			sp.Link = fmt.Sprintf("%s#t%d-%s", filePageName(otherFile), sp.TriggerIdx, otherRole)
		}
	}

	// Collect triggers relevant to this file (deduplicated by trigger index).
	seen := make(map[int]bool)
	var trows []triggerRow
	for _, sp := range fd.Spans {
		if seen[sp.TriggerIdx] {
			continue
		}
		seen[sp.TriggerIdx] = true
		e := registry.Triggers[sp.TriggerIdx]
		tr := triggerRow{
			Idx:          sp.TriggerIdx + 1,
			IsError:      e.IsError,
			ConsumerFile: filepath.Base(e.ConsumerFile),
			ConsumerLine: e.ConsumerLine,
			ConsumerLink: fmt.Sprintf("%s#t%d-cons", filePageName(e.ConsumerFile), sp.TriggerIdx),
			ProducerDesc: e.ProducerDesc,
			ConsumerDesc: e.ConsumerDesc,
		}
		if e.ProducerFile != "" {
			tr.ProducerFile = filepath.Base(e.ProducerFile)
			tr.ProducerLine = e.ProducerLine
			tr.ProducerLink = fmt.Sprintf("%s#t%d-prod", filePageName(e.ProducerFile), sp.TriggerIdx)
		}
		trows = append(trows, tr)
	}
	sort.Slice(trows, func(i, j int) bool { return trows[i].Idx < trows[j].Idx })

	d := filePageData{
		Filename:        fd.Filename,
		AnnotatedSource: template.HTML(annotateSource(fd)), //nolint:gosec
		Rows:            trows,
	}
	return fileTmpl.Execute(f, d)
}

// spanEvent represents a tag insertion point in the source byte stream.
type spanEvent struct {
	offset  int
	isOpen  bool
	tag     string
	spanIdx int // used for stable sort tie-breaking
}

// annotateSource produces HTML of the source file with <span> tags
// wrapping each highlighted region. Source bytes are HTML-escaped and
// wrapped in per-line <span class="line"> elements for CSS line numbering.
func annotateSource(fd *FileData) string {
	src := fd.Source
	if len(src) == 0 {
		return ""
	}

	// Build sorted list of open/close events.
	events := make([]spanEvent, 0, len(fd.Spans)*2)
	for i, sp := range fd.Spans {
		if sp.Start < 0 || sp.End <= sp.Start || sp.End > len(src) {
			continue
		}
		classes := template.HTMLEscapeString(spanClasses(sp))
		tooltip := template.HTMLEscapeString(sp.Tooltip)
		id := template.HTMLEscapeString(sp.ID)
		var openTag string
		if sp.Link != "" {
			openTag = fmt.Sprintf(`<a id="%s" class="%s" title="%s" href="%s">`,
				id, classes, tooltip, template.HTMLEscapeString(sp.Link))
		} else {
			openTag = fmt.Sprintf(`<a id="%s" class="%s" title="%s">`, id, classes, tooltip)
		}
		events = append(events, spanEvent{offset: sp.Start, isOpen: true, tag: openTag, spanIdx: i})
		events = append(events, spanEvent{offset: sp.End, isOpen: false, tag: "</a>", spanIdx: i})
	}

	// Sort: by offset; at the same offset, opens before closes.
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.offset != b.offset {
			return a.offset < b.offset
		}
		if a.isOpen != b.isOpen {
			return a.isOpen // open tags before close tags
		}
		return a.spanIdx < b.spanIdx
	})

	var buf bytes.Buffer
	buf.WriteString(`<span class="line">`)

	evIdx := 0
	for i, b := range src {
		// Emit any tags due at this byte offset.
		for evIdx < len(events) && events[evIdx].offset == i {
			buf.WriteString(events[evIdx].tag)
			evIdx++
		}

		// HTML-escape the source character.
		switch b {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '\n':
			buf.WriteString("</span>\n<span class=\"line\">")
		default:
			buf.WriteByte(b)
		}
	}

	// Emit any tags at the very end of the file.
	for evIdx < len(events) {
		buf.WriteString(events[evIdx].tag)
		evIdx++
	}

	buf.WriteString("</span>")
	return buf.String()
}

// spanClasses returns the CSS class string for a span.
func spanClasses(sp *SpanData) string {
	var sb strings.Builder
	if sp.IsProducer {
		sb.WriteString("producer")
	} else {
		sb.WriteString("consumer")
	}
	if sp.IsError {
		sb.WriteString(" trigger-error")
	} else {
		sb.WriteString(" trigger-safe")
	}
	return sb.String()
}
