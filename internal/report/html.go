package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/safeoutput"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

type reasonEntry struct {
	Code        string
	Description string
}

type dashboardHTMLView struct {
	reporttypes.Report
	Panels   []reporttypes.PanelRecord
	Glossary []reasonEntry
	Reviews  []reviewHTMLItem
	Chart    verdictChart
}

// verdictChart carries the pre-computed conic-gradient ring for the header
// donut. The gradient stops are computed in Go because html/template has no
// arithmetic, and the style is typed template.CSS so the attribute survives
// contextual escaping.
type verdictChart struct {
	Native      int
	Passthrough int
	Review      int
	Total       int
	NativeRate  string
	Style       template.CSS
}

type reviewHTMLItem struct {
	Panel       string
	Query       string
	ReasonCodes []string
	Explanation string
}

// WriteHTML writes a self-contained dashboard migration report.
func WriteHTML(path string, evidence reporttypes.Report) error {
	data, err := DashboardHTMLBytes(evidence)
	if err != nil {
		return err
	}
	return writeRenderedTemplate(path, data)
}

// DashboardHTMLBytes renders a self-contained dashboard migration report
// without publishing it. Artifact-set transactions use this to stage the HTML
// beside the exact JSON evidence and primary payload it describes.
func DashboardHTMLBytes(evidence reporttypes.Report) ([]byte, error) {
	view := dashboardHTMLView{Report: evidence, Panels: append([]reporttypes.PanelRecord(nil), evidence.Panels...)}
	slices.SortStableFunc(view.Panels, func(left, right reporttypes.PanelRecord) int {
		return verdictRank(left.Verdict) - verdictRank(right.Verdict)
	})
	for code, description := range evidence.ReasonCodes {
		view.Glossary = append(view.Glossary, reasonEntry{Code: code, Description: description})
	}
	for _, feature := range evidence.SourceFeatures {
		view.Reviews = append(view.Reviews, reviewHTMLItem{
			Panel: "Dashboard", Query: feature.SourcePath, ReasonCodes: []string{feature.ReasonCode},
			Explanation: firstReasonDescription(evidence.ReasonCodes, []string{feature.ReasonCode}),
		})
	}
	for _, variable := range evidence.Variables {
		residualReasons := variableReviewReasons(variable)
		if variable.Verdict == "needs_review" && len(residualReasons) > 0 {
			view.Reviews = append(view.Reviews, reviewHTMLItem{
				Panel: "Variable: " + variable.Name, Query: variable.SourcePath, ReasonCodes: residualReasons,
				Explanation: firstReasonDescription(evidence.ReasonCodes, residualReasons),
			})
		}
		for _, feature := range variable.SourceFeatures {
			view.Reviews = append(view.Reviews, reviewHTMLItem{
				Panel: "Variable: " + variable.Name, Query: feature.SourcePath, ReasonCodes: []string{feature.ReasonCode},
				Explanation: firstReasonDescription(evidence.ReasonCodes, []string{feature.ReasonCode}),
			})
		}
	}
	for _, panel := range evidence.Panels {
		for _, query := range panel.Queries {
			if query.Verdict != "needs_review" {
				continue
			}
			view.Reviews = append(view.Reviews, reviewHTMLItem{
				Panel: panel.Title, Query: query.RefID, ReasonCodes: query.ReasonCodes,
				Explanation: firstReasonDescription(evidence.ReasonCodes, query.ReasonCodes),
			})
		}
		if panel.Verdict == "needs_review" {
			panelOnlyReasons := panelReviewReasons(panel)
			if len(panelOnlyReasons) == 0 {
				continue
			}
			view.Reviews = append(view.Reviews, reviewHTMLItem{
				Panel: panel.Title, Query: panel.SourcePath, ReasonCodes: panelOnlyReasons,
				Explanation: firstReasonDescription(evidence.ReasonCodes, panelOnlyReasons),
			})
		}
	}
	slices.SortFunc(view.Glossary, func(left, right reasonEntry) int {
		return strings.Compare(left.Code, right.Code)
	})
	view.Chart = buildVerdictChart(evidence.Summary)

	parsed, err := template.New("dashboard-report").Funcs(template.FuncMap{
		"class":    statusClass,
		"json":     prettyJSON,
		"review":   func(verdict string) bool { return strings.EqualFold(verdict, "needs_review") },
		"cmpstats": comparisonStats,
		"spark":    sparkChart,
	}).Parse(dashboardHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse dashboard report template: %w", err)
	}
	return executeTemplate(parsed, view)
}

// comparisonView summarizes an attached differential comparison for the
// per-query fidelity meter. BarPercent scales the measured maximum relative
// error against a 5% full-scale so small errors stay visibly small.
type comparisonView struct {
	MatchedSeries   int     `json:"matchedSeries"`
	MatchedPoints   int     `json:"matchedPoints"`
	MaxRelativeErr  float64 `json:"maxRelativeError"`
	MaxRelativePct  float64 `json:"-"`
	BarPercent      float64 `json:"-"`
	WithinTolerance bool    `json:"-"`
}

func comparisonStats(raw json.RawMessage) *comparisonView {
	if len(raw) == 0 {
		return nil
	}
	var view comparisonView
	if err := json.Unmarshal(raw, &view); err != nil {
		return nil
	}
	if view.MatchedPoints == 0 && view.MatchedSeries == 0 {
		return nil
	}
	view.MaxRelativePct = view.MaxRelativeErr * 100
	view.BarPercent = view.MaxRelativeErr / 0.05 * 100
	if view.BarPercent > 100 {
		view.BarPercent = 100
	}
	view.WithinTolerance = view.MaxRelativeErr <= 0.05
	return &view
}

// sparkPalette cycles line colors for sampled series, mirroring the report's
// verdict palette so charts read as part of the same system.
var sparkPalette = []string{"#52d273", "#61a8ff", "#ff6b35", "#b48cff", "#4fd8cf", "#ff77a8"}

// sparkChart renders the sampled series a query actually returned on the live
// target as a self-contained inline SVG line chart with a legend. Everything
// is generated from finite numeric data; label text is HTML-escaped.
func sparkChart(samples []reporttypes.SeriesSample) template.HTML {
	const (
		width   = 640.0
		height  = 150.0
		left    = 52.0
		right   = 634.0
		top     = 10.0
		bottom  = 126.0
		timeRow = 144.0
	)
	minValue, maxValue := math.Inf(1), math.Inf(-1)
	minTime, maxTime := int64(math.MaxInt64), int64(math.MinInt64)
	total := 0
	for _, series := range samples {
		for _, point := range series.Points {
			minValue = math.Min(minValue, point.Value)
			maxValue = math.Max(maxValue, point.Value)
			minTime = min(minTime, point.Timestamp)
			maxTime = max(maxTime, point.Timestamp)
			total++
		}
	}
	if total < 2 || minTime == maxTime {
		return ""
	}
	if maxValue == minValue {
		pad := math.Max(math.Abs(maxValue)*0.1, 1)
		maxValue += pad
		minValue -= pad
	}
	scaleX := func(ts int64) float64 {
		return left + (right-left)*float64(ts-minTime)/float64(maxTime-minTime)
	}
	scaleY := func(value float64) float64 {
		return bottom - (bottom-top)*(value-minValue)/(maxValue-minValue)
	}

	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg viewBox="0 0 %.0f %.0f" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Sampled series from the live target">`, width, height)
	for _, fraction := range []float64{0, 0.5, 1} {
		y := top + (bottom-top)*fraction
		value := maxValue - (maxValue-minValue)*fraction
		fmt.Fprintf(&svg, `<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="#282e3b" stroke-width="1"/>`, left, y, right, y)
		fmt.Fprintf(&svg, `<text x="%.0f" y="%.1f" fill="#9ca6b8" font-size="10" text-anchor="end">%s</text>`, left-6, y+3.5, formatSampleValue(value))
	}
	fmt.Fprintf(&svg, `<text x="%.0f" y="%.0f" fill="#9ca6b8" font-size="10">%s</text>`, left, timeRow, sampleTimeLabel(minTime))
	fmt.Fprintf(&svg, `<text x="%.0f" y="%.0f" fill="#9ca6b8" font-size="10" text-anchor="end">%s</text>`, right, timeRow, sampleTimeLabel(maxTime))
	for index, series := range samples {
		if len(series.Points) == 0 {
			continue
		}
		color := sparkPalette[index%len(sparkPalette)]
		var points strings.Builder
		for _, point := range series.Points {
			fmt.Fprintf(&points, "%.1f,%.1f ", scaleX(point.Timestamp), scaleY(point.Value))
		}
		if len(series.Points) == 1 {
			only := series.Points[0]
			fmt.Fprintf(&svg, `<circle cx="%.1f" cy="%.1f" r="2.5" fill="%s"/>`, scaleX(only.Timestamp), scaleY(only.Value), color)
			continue
		}
		fmt.Fprintf(&svg, `<polyline class="spark-line" pathLength="100" points="%s" fill="none" stroke="%s" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"/>`, strings.TrimSpace(points.String()), color)
	}
	svg.WriteString(`</svg>`)

	var legend strings.Builder
	legend.WriteString(`<div class="chart-legend">`)
	for index, series := range samples {
		label := series.Labels
		if label == "" {
			label = fmt.Sprintf("series %d", index+1)
		}
		if len(label) > 64 {
			label = label[:61] + "…"
		}
		fmt.Fprintf(&legend, `<span><i style="background:%s"></i>%s</span>`,
			sparkPalette[index%len(sparkPalette)], template.HTMLEscapeString(label))
	}
	legend.WriteString(`</div>`)
	return template.HTML(svg.String() + legend.String()) // #nosec G203 -- numeric data and escaped labels only
}

func formatSampleValue(value float64) string {
	magnitude := math.Abs(value)
	switch {
	case magnitude >= 1e9:
		return fmt.Sprintf("%.1fG", value/1e9)
	case magnitude >= 1e6:
		return fmt.Sprintf("%.1fM", value/1e6)
	case magnitude >= 1e3:
		return fmt.Sprintf("%.1fk", value/1e3)
	case magnitude != 0 && magnitude < 0.01:
		return fmt.Sprintf("%.1e", value)
	default:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
	}
}

func sampleTimeLabel(epochMilliseconds int64) string {
	return time.UnixMilli(epochMilliseconds).UTC().Format("15:04:05")
}

func buildVerdictChart(summary reporttypes.Summary) verdictChart {
	chart := verdictChart{
		Native:      summary.Native,
		Passthrough: summary.Passthrough,
		Review:      summary.NeedsReview,
	}
	chart.Total = chart.Native + chart.Passthrough + chart.Review
	if chart.Total == 0 {
		return chart
	}
	nativeEnd := float64(chart.Native) / float64(chart.Total) * 100
	passEnd := float64(chart.Native+chart.Passthrough) / float64(chart.Total) * 100
	chart.NativeRate = fmt.Sprintf("%.0f%%", nativeEnd)
	chart.Style = template.CSS(fmt.Sprintf(
		"background:conic-gradient(var(--green) 0 %.2f%%,var(--blue) %.2f%% %.2f%%,var(--red) %.2f%% 100%%)",
		nativeEnd, nativeEnd, passEnd, passEnd))
	return chart
}

func variableReviewReasons(variable reporttypes.VariableRecord) []string {
	featureReasons := make(map[string]bool, len(variable.SourceFeatures))
	for _, feature := range variable.SourceFeatures {
		featureReasons[feature.ReasonCode] = true
	}
	reasons := make([]string, 0, len(variable.ReasonCodes))
	for _, reason := range variable.ReasonCodes {
		if !featureReasons[reason] {
			reasons = append(reasons, reason)
		}
	}
	return reasons
}

func panelReviewReasons(panel reporttypes.PanelRecord) []string {
	queryReasons := make(map[string]bool)
	for _, query := range panel.Queries {
		for _, reason := range query.ReasonCodes {
			queryReasons[reason] = true
		}
	}
	reasons := make([]string, 0, len(panel.ReasonCodes))
	for _, reason := range panel.ReasonCodes {
		if !queryReasons[reason] {
			reasons = append(reasons, reason)
		}
	}
	return reasons
}

func firstReasonDescription(index map[string]string, codes []string) string {
	for _, code := range codes {
		if description := index[code]; description != "" {
			return description
		}
	}
	return "Inspect the source and emitted artifact before accepting this migration."
}

func executeTemplate(parsed *template.Template, value any) ([]byte, error) {
	var output bytes.Buffer
	if err := parsed.Execute(&output, value); err != nil {
		return nil, fmt.Errorf("render report template: %w", err)
	}
	return output.Bytes(), nil
}

func writeRenderedTemplate(path string, data []byte) error {
	if err := ensureRegularOrAbsent(path); err != nil {
		return err
	}
	if err := safeoutput.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("publish report %q: %w", path, err)
	}
	return nil
}

func ensureRegularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect report destination %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse report destination %q: existing path is not a regular file", path)
	}
	return nil
}

func verdictRank(verdict string) int {
	switch strings.ToLower(verdict) {
	case "needs_review":
		return 0
	case "PASSTHROUGH":
		return 1
	default:
		return 2
	}
}

func statusClass(verdict string) string {
	switch strings.ToLower(verdict) {
	case "needs_review":
		return "review"
	case "passthrough":
		return "pass"
	default:
		return "native"
	}
}

func prettyJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "unable to encode detail"
	}
	return string(data)
}

const dashboardHTMLTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:">
<title>{{.Dashboard.Title}} · SigNoz migration report</title>
<style>
:root{color-scheme:dark;--bg:#0b0d12;--surface:#131720;--line:#282e3b;--text:#f2f4f8;--muted:#9ca6b8;--orange:#ff6b35;--red:#ff6577;--green:#52d273;--blue:#61a8ff}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.55 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{width:min(1120px,calc(100% - 32px));margin:48px auto 96px}header{border-bottom:1px solid var(--line);padding-bottom:28px;margin-bottom:28px}.eyebrow{color:var(--orange);font-size:12px;font-weight:700;letter-spacing:.14em;text-transform:uppercase}h1{font-size:clamp(30px,5vw,52px);line-height:1.05;margin:10px 0 14px;letter-spacing:-.035em}h2{font-size:24px;margin:42px 0 14px}h3{font-size:18px;margin:0}.muted,.path{color:var(--muted)}.path{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;overflow-wrap:anywhere}.metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px;margin:24px 0}.metric,.panel,.variable,.notice{background:var(--surface);border:1px solid var(--line);border-radius:10px}.metric{padding:16px}.metric strong{display:block;font-size:27px}.metric span{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.08em}.notice{padding:16px 18px;border-left:4px solid var(--red)}.panel{margin:12px 0;padding:18px}.panel-head{display:flex;align-items:flex-start;justify-content:space-between;gap:18px}.badges{display:flex;flex-wrap:wrap;gap:7px;margin:12px 0}.badge{display:inline-flex;border:1px solid var(--line);border-radius:999px;padding:3px 9px;font-size:11px;font-weight:700;letter-spacing:.03em}.badge.native{border-color:#265f36;color:var(--green)}.badge.pass{border-color:#28558a;color:var(--blue)}.badge.review{border-color:#783843;color:var(--red)}details{border-top:1px solid var(--line);padding-top:12px;margin-top:12px}summary{cursor:pointer;font-weight:650}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#090b0f;border:1px solid var(--line);border-radius:7px;padding:13px;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace}.query{margin:14px 0 0;padding:14px;border-left:2px solid var(--line)}.query.review{border-color:var(--red)}.query.pass{border-color:var(--blue)}.query.native{border-color:var(--green)}.query-title{display:flex;justify-content:space-between;gap:12px}.variables{display:grid;grid-template-columns:repeat(auto-fit,minmax(250px,1fr));gap:10px}.variable{padding:14px}.glossary,.review-table{width:100%;border-collapse:collapse}.glossary th,.glossary td,.review-table th,.review-table td{text-align:left;vertical-align:top;border-bottom:1px solid var(--line);padding:10px}.glossary th,.review-table th{color:var(--muted);font-size:12px;text-transform:uppercase}.glossary code{color:var(--orange);font-size:12px}.hero{display:flex;align-items:center;justify-content:space-between;gap:28px;flex-wrap:wrap}.chart{display:flex;align-items:center;gap:20px}.donut{width:158px;height:158px;border-radius:50%;position:relative;flex:none}.donut-center{position:absolute;inset:21px;border-radius:50%;background:var(--bg);display:flex;flex-direction:column;align-items:center;justify-content:center}.donut-center strong{font-size:31px;letter-spacing:-.02em}.donut-center span{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.1em}.legend{list-style:none;margin:0;padding:0;display:grid;gap:9px}.legend li{display:flex;align-items:center;gap:9px;color:var(--muted);font-size:13px}.legend strong{color:var(--text)}.dot{width:10px;height:10px;border-radius:3px;display:inline-block;flex:none}.dot.native{background:var(--green)}.dot.pass{background:var(--blue)}.dot.review{background:var(--red)}.metric{border-top:3px solid var(--line)}.metric.m-green{border-top-color:var(--green)}.metric.m-blue{border-top-color:var(--blue)}.metric.m-red{border-top-color:var(--red)}.metric.m-orange{border-top-color:var(--orange)}.panel-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(360px,1fr));gap:12px;align-items:start}.panel-grid .panel{margin:0}.panel{border-top:3px solid var(--line)}.panel.native{border-top-color:var(--green)}.panel.pass{border-top-color:var(--blue)}.panel.review{border-top-color:var(--red)}.fidelity{display:flex;align-items:center;gap:10px;margin-top:10px;font-size:12px;flex-wrap:wrap}.meter{flex:1;min-width:90px;height:8px;background:#090b0f;border:1px solid var(--line);border-radius:99px;overflow:hidden}.meter i{display:block;height:100%;border-radius:99px}.meter.good i{background:linear-gradient(90deg,var(--green),#8be09a)}.meter.bad i{background:linear-gradient(90deg,var(--orange),var(--red))}.chartbox{margin:12px 0 4px;background:#090b0f;border:1px solid var(--line);border-radius:7px;padding:10px 12px}.chartbox svg{display:block;width:100%;height:auto}.chart-legend{display:flex;flex-wrap:wrap;gap:5px 14px;margin-top:8px;font-size:11px;color:var(--muted)}.chart-legend i{width:9px;height:9px;border-radius:2px;display:inline-block;margin-right:5px;vertical-align:-1px}
@keyframes rise{from{opacity:0;transform:translateY(16px)}to{opacity:1;transform:none}}
@keyframes sweep{from{opacity:0;transform:rotate(-120deg) scale(.55)}to{opacity:1;transform:none}}
@keyframes drawline{from{stroke-dashoffset:100}to{stroke-dashoffset:0}}
@keyframes growbar{from{transform:scaleX(0)}to{transform:scaleX(1)}}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.25}}
header{animation:rise .55s ease both}.eyebrow::before{content:"";display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--green);margin-right:8px;animation:pulse 2s ease-in-out infinite}
.donut{animation:sweep .9s cubic-bezier(.22,.9,.3,1.05) both .15s}.legend li{animation:rise .5s ease both}.legend li:nth-child(1){animation-delay:.35s}.legend li:nth-child(2){animation-delay:.45s}.legend li:nth-child(3){animation-delay:.55s}
.metric{animation:rise .5s ease both}.metric:nth-child(1){animation-delay:.05s}.metric:nth-child(2){animation-delay:.12s}.metric:nth-child(3){animation-delay:.19s}.metric:nth-child(4){animation-delay:.26s}.metric:nth-child(5){animation-delay:.33s}.metric:nth-child(6){animation-delay:.4s}.metric:nth-child(7){animation-delay:.47s}
.panel-grid .panel{animation:rise .5s ease both}.panel-grid .panel:nth-child(2){animation-delay:.08s}.panel-grid .panel:nth-child(3){animation-delay:.16s}.panel-grid .panel:nth-child(4){animation-delay:.24s}.panel-grid .panel:nth-child(n+5){animation-delay:.3s}
.panel{transition:border-color .25s ease,background .25s ease}.panel:hover{border-color:#4a5468;background:#161b26}.panel:hover{border-top-color:inherit}
.spark-line{stroke-dasharray:100;stroke-dashoffset:100;animation:drawline 1.3s ease-out forwards .25s}
.meter i{transform-origin:left;animation:growbar .9s ease-out both .35s}
@supports (animation-timeline: view()){.spark-line{animation:drawline .9s ease-out both;animation-timeline:view();animation-range:entry 5% entry 55%}.panel-grid .panel:nth-child(n+5){animation:rise .5s ease both;animation-timeline:view();animation-range:entry 0% entry 30%}}
@media(prefers-reduced-motion:reduce){*,*::before{animation:none!important;transition:none!important}}
@media(max-width:620px){main{width:min(100% - 20px,1120px);margin-top:24px}.panel-head,.query-title{display:block}.badge{margin-top:6px}.review-table{display:block;overflow-x:auto}.hero{display:block}.chart{margin-top:20px}.panel-grid{grid-template-columns:1fr}}
</style>
</head>
<body><main>
<header><div class="hero"><div><div class="eyebrow">Migration evidence · schema {{.SchemaVersion}}</div><h1>{{.Dashboard.Title}}</h1><div class="muted">{{.Summary.Headline}}</div><div class="path">{{.Dashboard.Source}}</div><div class="muted">{{.Tool.Name}} {{.Tool.Version}} ({{.Tool.Commit}}){{if .Run.Target}} · {{.Run.Target}}{{end}}{{if .Run.StartedAt}} · {{.Run.StartedAt}}{{end}}</div></div>
{{if .Chart.Total}}<div class="chart" role="img" aria-label="Query verdicts: {{.Chart.Native}} native, {{.Chart.Passthrough}} passthrough, {{.Chart.Review}} needs review"><div class="donut" style="{{.Chart.Style}}"><div class="donut-center"><strong>{{.Chart.NativeRate}}</strong><span>native</span></div></div><ul class="legend"><li><i class="dot native"></i><strong>{{.Chart.Native}}</strong>&nbsp;native (verified)</li><li><i class="dot pass"></i><strong>{{.Chart.Passthrough}}</strong>&nbsp;PromQL passthrough</li><li><i class="dot review"></i><strong>{{.Chart.Review}}</strong>&nbsp;needs review</li></ul></div>{{end}}</div></header>
{{if .Run.Flags}}<h2>Run outcome</h2><pre>{{json .Run.Flags}}</pre>{{end}}
{{if .PrimaryArtifact}}<h2>Primary dashboard artifact</h2><pre>{{json .PrimaryArtifact}}</pre>{{end}}
{{if .Differential}}<h2>Differential run provenance</h2><pre>{{json .Differential}}</pre>{{end}}
<section class="metrics" aria-label="Migration summary">
<div class="metric m-orange"><strong>{{.Summary.PanelsAccounted}}/{{.Summary.Panels}}</strong><span>Panels accounted</span></div>
<div class="metric m-green"><strong>{{.Summary.Builder}}</strong><span>Builder queries</span></div>
<div class="metric m-green"><strong>{{.Summary.Formula}}</strong><span>Builder formulas</span></div>
<div class="metric m-blue"><strong>{{.Summary.Passthrough}}</strong><span>PromQL passthrough</span></div>
<div class="metric m-red"><strong>{{.Summary.NeedsReview}}</strong><span>Queries to review</span></div>
<div class="metric m-orange"><strong>{{.Summary.PreviewValid}}/{{.Summary.Previewed}}</strong><span>Target previews valid</span></div>
<div class="metric m-orange"><strong>{{printf "%.1f" .Summary.DataPresentPercent}}%</strong><span>Eligible queries with data</span></div>
</section>
{{if or .Summary.PanelsNeedsReview .Summary.VariablesNeedsReview .Summary.NeedsReview .Summary.SourceFeaturesNeedsReview}}<div class="notice"><strong>Review is required before relying on this dashboard.</strong><div class="muted">The affected dashboard settings, panels, variables, and queries are listed first. Every captured source object and source-only field is reconciled below as an emitted representation, explicit review record, or deliberate omission.</div></div>{{end}}
{{if .Reviews}}<h2>Needs review</h2><table class="review-table"><thead><tr><th>Panel</th><th>Query</th><th>Reason codes</th><th>Why</th></tr></thead><tbody>{{range .Reviews}}<tr><td>{{.Panel}}</td><td>{{.Query}}</td><td>{{range .ReasonCodes}}<code>{{.}}</code><br>{{end}}</td><td>{{.Explanation}}</td></tr>{{end}}</tbody></table>{{end}}
{{if .SourceFeatures}}<h2>Dashboard source features</h2><pre>{{json .SourceFeatures}}</pre>{{end}}
<h2>Panels</h2>
<section class="panel-grid">
{{range .Panels}}{{$panel := .}}<article class="panel {{class .Verdict}}">
<div class="panel-head"><div><h3>{{.Title}}</h3><div class="path">{{.SourcePath}}</div></div><span class="badge {{class .Verdict}}">{{.Verdict}}</span></div>
<div class="badges"><span class="badge">{{.Kind}} → {{.EmittedKind}}</span><span class="badge">{{.EmittedMode}}</span><span class="badge">{{.State}}</span>{{range .ReasonCodes}}<span class="badge {{class $panel.Verdict}}">{{.}}</span>{{end}}</div>
{{if .Content}}<details><summary>Source panel content</summary><pre>{{.Content}}</pre></details>{{end}}
{{range .Queries}}{{$query := .}}<div class="query {{class .Verdict}}"><div class="query-title"><strong>Query {{.RefID}} · {{.CandidateKind}} → {{.EmittedKind}}</strong><span class="badge {{class .Verdict}}">{{.Verdict}}</span></div>
<div class="badges">{{range .ReasonCodes}}<span class="badge {{class $query.Verdict}}">{{.}}</span>{{end}}{{if .Validation.Executed}}<span class="badge">{{.Validation.Series}} series · {{.Validation.Points}} pts</span>{{end}}</div>
{{if .Validation.Samples}}{{with spark .Validation.Samples}}<div class="chartbox">{{.}}</div>{{end}}{{end}}
<details {{if review .Verdict}}open{{end}}><summary>Source and emitted query</summary>{{if .Format}}<div class="muted">Grafana query format: <code>{{.Format}}</code></div>{{end}}{{if .Step}}<div class="muted">Grafana target step: <code>{{.Step}}</code></div>{{end}}{{if .SourceFeatures}}<div class="muted">Unmapped query configuration</div><pre>{{json .SourceFeatures}}</pre>{{end}}<div class="muted">Source</div><pre>{{.Original}}</pre>{{if .PromQL}}<div class="muted">Emitted PromQL</div><pre>{{.PromQL}}</pre>{{end}}{{if .Builder}}<div class="muted">Builder candidate</div><pre>{{json .Builder}}</pre>{{end}}{{if .Formula}}<div class="muted">Formula candidate</div><pre>{{json .Formula}}</pre>{{end}}{{if .ParseErrors}}<div class="muted">Parse errors</div><pre>{{json .ParseErrors}}</pre>{{end}}</details>
{{with cmpstats .Comparison}}<div class="fidelity"><span class="muted">Differential fidelity</span><div class="meter {{if .WithinTolerance}}good{{else}}bad{{end}}"><i style="width:{{printf "%.1f" .BarPercent}}%"></i></div><span class="muted">{{.MatchedSeries}} series · {{.MatchedPoints}} pts · max err {{printf "%.3f" .MaxRelativePct}}%</span></div>{{end}}
{{if .Comparison}}<details><summary>Differential comparison</summary><pre>{{json .Comparison}}</pre></details>{{end}}
{{if .Validation.Previewed}}<details><summary>Live validation</summary><div class="badges"><span class="badge {{if .Validation.PreviewOK}}native{{else}}review{{end}}">Preview {{if .Validation.PreviewOK}}valid{{else}}invalid{{end}}</span>{{if .Validation.Executed}}<span class="badge {{if .Validation.DataPresent}}native{{else}}pass{{end}}">{{if .Validation.DataPresent}}data present{{else}}no data in window{{end}}</span>{{end}}</div>{{if .Validation.Error}}<pre>{{.Validation.ErrorCode}}: {{.Validation.Error}}</pre>{{end}}<div class="muted">{{.Validation.Series}} series · {{.Validation.Points}} points · {{.Validation.Rows}} rows · {{.Validation.CheckedAt}}</div></details>{{end}}</div>{{end}}
</article>{{end}}
</section>
{{if .Variables}}<h2>Variables</h2><section class="variables">{{range .Variables}}{{$variable := .}}<article class="variable"><div class="panel-head"><strong>{{.Name}}</strong><span class="badge {{class .Verdict}}">{{.Verdict}}</span></div><div class="muted">{{.SourceKind}} → {{.EmittedKind}}{{if .Attribute}} · {{.Attribute}}{{end}}{{if .Label}} · label: {{.Label}}{{end}}</div><div class="badges">{{range .ReasonCodes}}<span class="badge {{class $variable.Verdict}}">{{.}}</span>{{end}}</div>{{if .AllValue}}<div class="muted">Grafana All value: <code>{{.AllValue}}</code></div>{{end}}{{if .Current}}<div class="muted">Current: <code>{{json .Current}}</code></div>{{end}}{{if .SourceFeatures}}<div class="muted">Unmapped variable configuration</div><pre>{{json .SourceFeatures}}</pre>{{end}}{{range .Notes}}<div class="muted">{{.}}</div>{{end}}<div class="path">{{.SourcePath}}</div></article>{{end}}</section>{{end}}
<h2>Reason-code glossary</h2><table class="glossary"><thead><tr><th>Code</th><th>Meaning</th></tr></thead><tbody>{{range .Glossary}}<tr><td><code>{{.Code}}</code></td><td>{{.Description}}</td></tr>{{end}}</tbody></table>
</main></body></html>`
