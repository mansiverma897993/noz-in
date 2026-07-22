package transpile

// This file defines the analyzer options and the public entry points for
// parsing and classifying PromQL expressions.

import (
	"slices"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/prometheus/prometheus/promql/parser"
)

// Options controls deterministic Grafana global rewrites.
type Options struct {
	RateInterval   time.Duration
	Interval       time.Duration
	Range          time.Duration
	LabelMap       map[string]string
	Metrics        map[string]model.TargetMetric
	MissingMetrics map[string]bool
	MetadataErrors map[string]bool
	RecordingRules map[string]model.Rule
}

// Analyzer parses and classifies PromQL expressions.
type Analyzer struct {
	parser   parser.Parser
	options  Options
	labelMap map[string]string
}

// TargetLabel returns the target field corresponding to a source label.
func (analyzer *Analyzer) TargetLabel(source string) string {
	if target, ok := analyzer.labelMap[source]; ok {
		return target
	}
	return source
}

// NewAnalyzer constructs a PromQL analyzer.
func NewAnalyzer(options Options) *Analyzer {
	if options.RateInterval <= 0 {
		options.RateInterval = 5 * time.Minute
	}
	if options.Interval <= 0 {
		options.Interval = time.Minute
	}
	options.Interval = max(options.Interval.Truncate(time.Second), time.Minute)
	if options.Range <= 0 {
		options.Range = time.Hour
	}
	labelMap := options.LabelMap
	if labelMap == nil {
		labelMap = map[string]string{
			"instance": "service.instance.id",
			"job":      "service.name",
		}
	}
	return &Analyzer{
		parser:   parser.NewParser(parser.Options{}),
		options:  options,
		labelMap: labelMap,
	}
}

// Analyze returns the safest target representation for a query.
func (analyzer *Analyzer) Analyze(query model.Query) model.Translation {
	effective := *analyzer
	intervalControlled := effective.applyQueryIntervalControls(query)
	hidden := query.Hidden
	query.Hidden = false
	translation := effective.analyze(query)
	normalizedLegend := effective.normalizeLegend(query.Legend)
	translation.Legend = &normalizedLegend
	if intervalControlled {
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = uniqueReasons(append(translation.Decision.Reasons, model.ReasonGrafanaIntervalControl))
	}
	if grafanaQueryFormatRequiresReview(query.Format) {
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = uniqueReasons(append(translation.Decision.Reasons, model.ReasonGrafanaQueryFormat))
	}
	for _, feature := range query.SourceFeatures {
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = uniqueReasons(append(translation.Decision.Reasons, feature.Reason))
	}
	if hidden {
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = uniqueReasons(append(translation.Decision.Reasons, model.ReasonHiddenTarget))
	}
	if query.RefIDNormalized {
		translation.Decision.Reasons = uniqueReasons(append(translation.Decision.Reasons, model.ReasonRefIDNormalized))
	}
	return translation
}

// IsStaticallyNonExecutable reports whether a query is guaranteed to emit no
// target query without consulting live metric metadata. It deliberately stops
// before output-shape and metric qualification checks, which metadata can
// change.
func (analyzer *Analyzer) IsStaticallyNonExecutable(query model.Query) bool {
	if _, excluded := analysisExclusion(query); excluded {
		return true
	}
	prepared := analyzer.prepareExpression(query.Expression)
	if !prepared.executable {
		return true
	}
	_, err := analyzer.parser.ParseExpr(prepared.parse)
	return err != nil
}

// MetricNames returns every metric selector that can be resolved against live target metadata.
func (analyzer *Analyzer) MetricNames(query model.Query) []string {
	if analyzer.IsStaticallyNonExecutable(query) {
		return nil
	}
	prepared := analyzer.prepareExpression(query.Expression)
	if !prepared.executable {
		return nil
	}
	expr, err := analyzer.parser.ParseExpr(prepared.parse)
	if err != nil {
		return nil
	}
	expr, _ = analyzer.inlineRecordingRules(expr)
	names := make([]string, 0)
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		name := metricName(selector)
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
		return nil
	})
	slices.Sort(names)
	return names
}

// MetadataMetricName returns the one metric whose target metadata can unlock a simple query.
func (analyzer *Analyzer) MetadataMetricName(query model.Query) (string, bool) {
	if !isPrometheusDatasource(query.Datasource) || strings.TrimSpace(query.Expression) == "" {
		return "", false
	}
	prepared := analyzer.prepareExpression(query.Expression)
	expr, err := analyzer.parser.ParseExpr(prepared.parse)
	if err != nil {
		return "", false
	}
	expr, _ = analyzer.inlineRecordingRules(expr)
	return metadataMetricName(expr)
}
