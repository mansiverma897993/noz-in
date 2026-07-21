package report

import (
	"fmt"
	"html/template"
	"slices"
	"strings"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

type ruleHTMLRecord struct {
	Group reporttypes.RuleGroupRecord
	reporttypes.RuleRecord
}

type ruleHTMLView struct {
	reporttypes.RuleReport
	Rules    []ruleHTMLRecord
	Glossary []reasonEntry
}

// WriteRulesHTML writes a self-contained Prometheus rule migration report.
func WriteRulesHTML(path string, evidence reporttypes.RuleReport) error {
	data, err := RulesHTMLBytes(evidence)
	if err != nil {
		return err
	}
	return writeRenderedTemplate(path, data)
}

// RulesHTMLBytes renders a self-contained rule migration report without
// publishing it, allowing the complete generation to be staged atomically.
func RulesHTMLBytes(evidence reporttypes.RuleReport) ([]byte, error) {
	view := ruleHTMLView{RuleReport: evidence}
	for _, group := range evidence.Groups {
		for _, rule := range group.Rules {
			view.Rules = append(view.Rules, ruleHTMLRecord{Group: group, RuleRecord: rule})
		}
	}
	slices.SortStableFunc(view.Rules, func(left, right ruleHTMLRecord) int {
		return verdictRank(left.Verdict) - verdictRank(right.Verdict)
	})
	for code, description := range evidence.ReasonCodes {
		view.Glossary = append(view.Glossary, reasonEntry{Code: code, Description: description})
	}
	slices.SortFunc(view.Glossary, func(left, right reasonEntry) int {
		return strings.Compare(left.Code, right.Code)
	})
	parsed, err := template.New("rule-report").Funcs(template.FuncMap{
		"class":      statusClass,
		"review":     func(verdict string) bool { return strings.EqualFold(verdict, "needs_review") },
		"json":       prettyJSON,
		"writeClass": ruleWriteStatusClass,
	}).Parse(ruleHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse rule report template: %w", err)
	}
	return executeTemplate(parsed, view)
}

func ruleWriteStatusClass(write *reporttypes.RuleWriteRecord) string {
	if write == nil {
		return "pass"
	}
	if write.Succeeded {
		return "native"
	}
	if write.Attempted || write.Error != "" {
		return "review"
	}
	return "pass"
}

const ruleHTMLTemplate = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'"><title>Prometheus rules · SigNoz migration report</title>
<style>:root{color-scheme:dark;--bg:#0b0d12;--surface:#131720;--line:#282e3b;--text:#f2f4f8;--muted:#9ca6b8;--orange:#ff6b35;--red:#ff6577;--green:#52d273;--blue:#61a8ff}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.55 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{width:min(1100px,calc(100% - 32px));margin:48px auto 96px}header{border-bottom:1px solid var(--line);padding-bottom:24px}h1{font-size:clamp(32px,5vw,50px);letter-spacing:-.035em;margin:8px 0}h2{margin-top:40px}.eyebrow{color:var(--orange);font-size:12px;font-weight:700;letter-spacing:.14em;text-transform:uppercase}.muted,.path{color:var(--muted)}.path{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;overflow-wrap:anywhere}.metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:10px;margin:24px 0}.metric,.rule{background:var(--surface);border:1px solid var(--line);border-radius:10px}.metric{padding:15px}.metric strong{display:block;font-size:26px}.metric span{color:var(--muted);font-size:11px;text-transform:uppercase}.rule{padding:18px;margin:12px 0}.head{display:flex;justify-content:space-between;gap:16px}.badges{display:flex;flex-wrap:wrap;gap:7px;margin:10px 0}.badge{border:1px solid var(--line);border-radius:999px;padding:3px 9px;font-size:11px;font-weight:700}.badge.native{color:var(--green);border-color:#265f36}.badge.pass{color:var(--blue);border-color:#28558a}.badge.review{color:var(--red);border-color:#783843}details{border-top:1px solid var(--line);padding-top:11px;margin-top:11px}summary{cursor:pointer;font-weight:650}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#090b0f;border:1px solid var(--line);border-radius:7px;padding:13px;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace}.glossary{width:100%;border-collapse:collapse}.glossary th,.glossary td{text-align:left;vertical-align:top;border-bottom:1px solid var(--line);padding:10px}.glossary code{color:var(--orange)}@media(max-width:620px){main{width:calc(100% - 20px);margin-top:24px}.head{display:block}}</style></head>
<body><main><header><div class="eyebrow">Migration evidence · schema {{.SchemaVersion}}</div><h1>Prometheus rules</h1><div class="path">{{.Source.Path}}</div></header>
{{if .PrimaryArtifact}}<h2>Primary rules artifact</h2><pre>{{json .PrimaryArtifact}}</pre>{{end}}
<section class="metrics"><div class="metric"><strong>{{.Summary.Rules}}</strong><span>Rules accounted</span></div><div class="metric"><strong>{{.Summary.Emitted}}</strong><span>Alerts emitted</span></div><div class="metric"><strong>{{.Summary.Enabled}}</strong><span>Enabled</span></div><div class="metric"><strong>{{.Summary.Disabled}}</strong><span>Disabled</span></div><div class="metric"><strong>{{.Summary.NotCreatedDisabled}}</strong><span>Disabled candidates not created</span></div><div class="metric"><strong>{{.Summary.NeedsReview}}</strong><span>Needs review</span></div><div class="metric"><strong>{{.Summary.PreviewValid}}/{{.Summary.Previewed}}</strong><span>Previews valid</span></div></section>
<h2>Rules</h2>{{range .Rules}}{{$rule := .}}<article class="rule"><div class="head"><div><strong>{{if .Alert}}{{.Alert}}{{else}}{{.Record}}{{end}}</strong><div class="muted">{{.Group.Name}}</div><div class="path">{{.SourcePath}}</div></div><span class="badge {{class .Verdict}}">{{.Verdict}}</span></div><div class="badges">{{if .Alert}}<span class="badge">alert</span>{{else}}<span class="badge">recording rule</span>{{end}}{{if .Group.Interval}}<span class="badge">interval {{.Group.Interval}}</span>{{end}}{{if .Group.QueryOffset}}<span class="badge">query offset {{.Group.QueryOffset}}</span>{{end}}{{if .Group.Limit}}<span class="badge">limit {{.Group.Limit}}</span>{{end}}{{if .RequireMinPoints}}<span class="badge">minimum points {{.RequiredNumPoints}}</span>{{end}}{{range .ReasonCodes}}<span class="badge {{class $rule.Verdict}}">{{.}}</span>{{end}}</div>{{if .Group.Labels}}<details><summary>Source group labels</summary><pre>{{json .Group.Labels}}</pre></details>{{end}}<details {{if review .Verdict}}open{{end}}><summary>Source and emitted expression</summary><div class="muted">Source</div><pre>{{.Original}}</pre>{{if .PromQL}}<div class="muted">Emitted PromQL</div><pre>{{.PromQL}}</pre>{{end}}{{if .Notes}}<div class="muted">Notes</div><pre>{{range .Notes}}{{.}}
{{end}}</pre>{{end}}</details>{{if .Validation.Previewed}}<details><summary>Live validation</summary><div class="badges"><span class="badge {{if .Validation.PreviewOK}}native{{else}}review{{end}}">Preview {{if .Validation.PreviewOK}}valid{{else}}invalid{{end}}</span>{{if .Validation.Executed}}<span class="badge {{if .Validation.DataPresent}}native{{else}}pass{{end}}">{{if .Validation.DataPresent}}data present{{else}}no data in window{{end}}</span>{{end}}</div>{{if .Validation.Error}}<pre>{{.Validation.ErrorCode}}: {{.Validation.Error}}</pre>{{end}}</details>{{end}}{{if .Write}}<details><summary>Target write outcome</summary><div class="badges"><span class="badge {{writeClass .Write}}">{{.Write.Action}}</span><span class="badge">requested: {{.Write.Requested}}</span><span class="badge">attempted: {{.Write.Attempted}}</span><span class="badge">succeeded: {{.Write.Succeeded}}</span></div>{{if .Write.ID}}<div class="path">Target rule ID: {{.Write.ID}}</div>{{end}}{{if .Write.Error}}<pre>{{.Write.Error}}</pre>{{end}}</details>{{end}}</article>{{end}}
<h2>Reason-code glossary</h2><table class="glossary"><thead><tr><th>Code</th><th>Meaning</th></tr></thead><tbody>{{range .Glossary}}<tr><td><code>{{.Code}}</code></td><td>{{.Description}}</td></tr>{{end}}</tbody></table></main></body></html>`
