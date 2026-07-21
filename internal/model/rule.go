package model

import "maps"

// RuleSet is a normalized Prometheus rule file.
type RuleSet struct {
	Source Source      `json:"source"`
	Groups []RuleGroup `json:"groups"`
}

// RuleGroup preserves Prometheus evaluation grouping and provenance.
type RuleGroup struct {
	Name        string            `json:"name"`
	Interval    string            `json:"interval,omitempty"`
	QueryOffset string            `json:"queryOffset,omitempty"`
	Limit       int               `json:"limit"`
	Labels      map[string]string `json:"labels,omitempty"`
	SourcePath  string            `json:"sourcePath"`
	Rules       []Rule            `json:"rules"`
}

// EffectiveLabels applies Prometheus's group-first, rule-last label precedence
// without mutating either source map.
func (group RuleGroup) EffectiveLabels(rule Rule) map[string]string {
	if len(group.Labels) == 0 && len(rule.Labels) == 0 {
		return nil
	}
	result := make(map[string]string, len(group.Labels)+len(rule.Labels))
	maps.Copy(result, group.Labels)
	maps.Copy(result, rule.Labels)
	return result
}

// Rule is one normalized Prometheus alerting or recording rule.
type Rule struct {
	Alert         string            `json:"alert,omitempty"`
	Record        string            `json:"record,omitempty"`
	Expression    string            `json:"expression"`
	For           string            `json:"for,omitempty"`
	KeepFiringFor string            `json:"keepFiringFor,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
	SourcePath    string            `json:"sourcePath"`
}

// IsAlerting reports whether this rule creates an alert.
func (rule Rule) IsAlerting() bool {
	return rule.Alert != ""
}

// IsRecording reports whether this rule creates a stored time series.
func (rule Rule) IsRecording() bool {
	return rule.Record != ""
}
