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

import "html/template"

const _indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>NilAway Analysis Report</title>
<style>
body { font-family: sans-serif; margin: 2em; color: #222; }
h1 { border-bottom: 2px solid #333; padding-bottom: 0.3em; }
.summary { background: #f5f5f5; border: 1px solid #ddd; padding: 1em; border-radius: 4px; margin-bottom: 1.5em; }
.summary span.errors { color: #C62828; font-weight: bold; }
.summary span.safe   { color: #2E7D32; font-weight: bold; }
table { border-collapse: collapse; width: 100%; }
th { background: #eeeeee; text-align: left; padding: 0.4em 0.8em; border: 1px solid #ccc; }
td { padding: 0.4em 0.8em; border: 1px solid #ddd; vertical-align: top; }
tr:hover td { background: #fafafa; }
.err  { color: #C62828; font-weight: bold; }
.safe { color: #2E7D32; }
a { color: #1565C0; }
</style>
</head>
<body>
<h1>NilAway Analysis Report</h1>
<div class="summary">
  <p>Analyzed <strong>{{.TotalFiles}}</strong> file(s) with triggers &mdash;
     <span class="errors">{{.TotalErrors}} error trigger(s)</span>,
     <span class="safe">{{.TotalSafe}} safe trigger(s)</span>
     ({{.TotalTriggers}} total).
  </p>
</div>
<table>
<thead>
  <tr>
    <th>File</th>
    <th>Errors</th>
    <th>Safe</th>
    <th>Total</th>
  </tr>
</thead>
<tbody>
{{range .Rows}}
  <tr>
    <td><a href="{{.Link}}">{{.Filename}}</a></td>
    <td class="{{if gt .Errors 0}}err{{end}}">{{.Errors}}</td>
    <td class="safe">{{.Safe}}</td>
    <td>{{.Total}}</td>
  </tr>
{{end}}
</tbody>
</table>
</body>
</html>`

// indexTmpl is parsed once at init time.
var indexTmpl = template.Must(template.New("index").Parse(_indexHTML))

const _fileHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>{{.Filename}} — NilAway</title>
<style>
body { font-family: sans-serif; margin: 2em; color: #222; }
h1   { border-bottom: 2px solid #333; padding-bottom: 0.3em; font-size: 1.1em; }
a    { color: #1565C0; }
nav  { margin-bottom: 1.5em; font-size: 0.9em; }

/* Source code block */
pre {
  font-family: monospace;
  font-size: 0.92em;
  tab-size: 4;
  overflow-x: auto;
  background: #FAFAFA;
  border: 1px solid #DDD;
  padding: 1em;
  line-height: 1.5;
  counter-reset: line;
}
.line { display: block; }
.line::before {
  counter-increment: line;
  content: counter(line);
  display: inline-block;
  width: 3.5em;
  margin-right: 1em;
  text-align: right;
  color: #999;
  user-select: none;
}

/* Highlight classes */
.producer { background: #FFF3E0; }       /* orange tint */
.consumer { background: #E3F2FD; }       /* blue tint   */
.trigger-error { outline: 2px solid #C62828; outline-offset: -1px; }
.trigger-safe  { outline: 2px solid #2E7D32; outline-offset: -1px; }

/* Legend */
.legend { margin: 1em 0; font-size: 0.88em; }
.legend span { display: inline-block; padding: 2px 8px; margin-right: 0.5em; border-radius: 3px; }

/* Trigger table */
h2 { margin-top: 2em; }
table { border-collapse: collapse; width: 100%; font-size: 0.88em; }
th { background: #eeeeee; text-align: left; padding: 0.4em 0.8em; border: 1px solid #ccc; }
td { padding: 0.4em 0.8em; border: 1px solid #ddd; vertical-align: top; }
tr:hover td { background: #fafafa; }
.err  { color: #C62828; font-weight: bold; }
.safe { color: #2E7D32; }
</style>
</head>
<body>
<nav><a href="index.html">&#8592; Back to index</a></nav>
<h1>{{.Filename}}</h1>
<div class="legend">
  <span class="producer trigger-safe">producer (safe)</span>
  <span class="producer trigger-error">producer (error)</span>
  <span class="consumer trigger-safe">consumer (safe)</span>
  <span class="consumer trigger-error">consumer (error)</span>
</div>

<pre>{{.AnnotatedSource}}</pre>

<h2>Triggers in this file ({{len .Rows}} total)</h2>
<table>
<thead>
  <tr>
    <th>#</th>
    <th>Status</th>
    <th>Producer</th>
    <th>Consumer</th>
    <th>Producer description</th>
    <th>Consumer description</th>
  </tr>
</thead>
<tbody>
{{range .Rows}}
  <tr>
    <td>{{.Idx}}</td>
    <td class="{{if .IsError}}err{{else}}safe{{end}}">{{if .IsError}}ERROR{{else}}SAFE{{end}}</td>
    <td>{{if .ProducerFile}}<a href="{{.ProducerLink}}">{{.ProducerFile}}:{{.ProducerLine}}</a>{{else}}&mdash;{{end}}</td>
    <td><a href="{{.ConsumerLink}}">{{.ConsumerFile}}:{{.ConsumerLine}}</a></td>
    <td>{{.ProducerDesc}}</td>
    <td>{{.ConsumerDesc}}</td>
  </tr>
{{end}}
</tbody>
</table>
</body>
</html>`

// fileTmpl is parsed once at init time.
var fileTmpl = template.Must(template.New("file").Parse(_fileHTML))
