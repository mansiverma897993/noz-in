package transpile

// This file handles Grafana-specific concerns: datasource classification,
// panel interval controls, and rewriting of Grafana global variables.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/model"
	prommodel "github.com/prometheus/common/model"
)

func grafanaQueryFormatRequiresReview(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "time_series":
		return false
	default:
		return true
	}
}

func (analyzer *Analyzer) applyQueryIntervalControls(query model.Query) bool {
	controlled := query.Step > 0 || strings.TrimSpace(query.Interval) != "" || query.IntervalFactor > 0 || query.MaxDataPoints > 0
	if !controlled {
		return false
	}
	// Grafana exports target step alongside interval, intervalFactor, and
	// maxDataPoints without a stable precedence contract. Preserve step as
	// evidence and require review, but do not let it silently override the
	// existing target-step derivation.
	interval := analyzer.options.Interval
	factor := max(query.IntervalFactor, 1)
	if query.MaxDataPoints > 0 {
		resolution := analyzer.options.Range / time.Duration(query.MaxDataPoints)
		if resolution%time.Second != 0 {
			resolution = (resolution/time.Second + 1) * time.Second
		}
		interval = max(interval, scaleDuration(resolution, factor))
	} else if query.IntervalFactor > 1 {
		interval = scaleDuration(interval, factor)
	}
	if minimum, ok := parseGrafanaInterval(query.Interval); ok {
		interval = max(interval, minimum)
	}
	analyzer.options.Interval = max(interval, time.Minute)
	return true
}

func scaleDuration(value time.Duration, factor int) time.Duration {
	if factor <= 1 {
		return value
	}
	if value > time.Duration(1<<63-1)/time.Duration(factor) {
		return time.Duration(1<<63 - 1)
	}
	return value * time.Duration(factor)
}

func parseGrafanaInterval(value string) (time.Duration, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), ">"))
	if value == "" {
		return 0, false
	}
	parsed, err := prommodel.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return time.Duration(parsed), true
}

func isGrafanaExpression(query model.Query) bool {
	return strings.EqualFold(query.Datasource.Type, "__expr__") ||
		strings.EqualFold(query.Datasource.UID, "__expr__") ||
		strings.EqualFold(query.QueryType, "expression") ||
		strings.EqualFold(query.QueryType, "math") ||
		strings.EqualFold(query.QueryType, "reduce") ||
		strings.EqualFold(query.QueryType, "threshold")
}

func (analyzer *Analyzer) rewriteGlobals(expression string) (string, []model.ReasonCode) {
	rewritten := analyzer.rewriteDurationGlobals(expression)
	rewritten = replaceGrafanaGlobal(rewritten, "__from", "$"+targetStartTimeVariable)
	rewritten = replaceGrafanaGlobal(rewritten, "__to", "$"+targetEndTimeVariable)
	if rewritten == expression {
		return expression, nil
	}
	return rewritten, []model.ReasonCode{model.ReasonRateIntervalRewrite}
}

func (analyzer *Analyzer) rewriteDurationGlobals(expression string) string {
	replacements := []struct {
		name  string
		value string
	}{
		{"__rate_interval_ms", durationScalar(analyzer.options.RateInterval, time.Millisecond)},
		{"__rate_interval_s", durationScalar(analyzer.options.RateInterval, time.Second)},
		{"__interval_ms", durationScalar(analyzer.options.Interval, time.Millisecond)},
		{"__interval_s", durationScalar(analyzer.options.Interval, time.Second)},
		{"__range_ms", durationScalar(analyzer.options.Range, time.Millisecond)},
		{"__range_s", durationScalar(analyzer.options.Range, time.Second)},
		{"__rate_interval", promDuration(analyzer.options.RateInterval)},
		{"__interval", promDuration(analyzer.options.Interval)},
		{"__range", promDuration(analyzer.options.Range)},
	}
	rewritten := expression
	for _, replacement := range replacements {
		rewritten = replaceGrafanaGlobal(rewritten, replacement.name, replacement.value)
	}
	return rewritten
}

func replaceGrafanaGlobal(expression, name, value string) string {
	expression = strings.ReplaceAll(expression, "${"+name+"}", value)
	expression = strings.ReplaceAll(expression, "[["+name+"]]", value)
	token := "$" + name
	if !strings.Contains(expression, token) {
		return expression
	}
	var result strings.Builder
	remaining := expression
	for {
		index := strings.Index(remaining, token)
		if index < 0 {
			result.WriteString(remaining)
			return result.String()
		}
		result.WriteString(remaining[:index])
		end := index + len(token)
		if end < len(remaining) && isGrafanaIdentifierByte(remaining[end]) {
			result.WriteString(token)
			remaining = remaining[end:]
			continue
		}
		result.WriteString(value)
		remaining = remaining[end:]
	}
}

func isGrafanaIdentifierByte(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func durationScalar(value, unit time.Duration) string {
	return strconv.FormatFloat(float64(value)/float64(unit), 'f', -1, 64)
}

func promDuration(value time.Duration) string {
	if value%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(value/time.Hour))
	}
	if value%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(value/time.Minute))
	}
	return fmt.Sprintf("%ds", int(value/time.Second))
}

func isPrometheusDatasource(datasource model.Datasource) bool {
	if datasource == (model.Datasource{}) {
		return true
	}
	typeName := strings.ToLower(strings.TrimSpace(datasource.Type))
	if typeName != "" {
		return strings.Contains(typeName, "prometheus")
	}
	joined := strings.ToLower(datasource.Name + " " + datasource.UID)
	if strings.Contains(joined, "loki") || strings.Contains(joined, "cloudwatch") || strings.Contains(joined, "elasticsearch") {
		return false
	}
	if strings.Contains(joined, "prometheus") || strings.Contains(joined, "${") || strings.HasPrefix(joined, "$") {
		return true
	}
	return false
}
