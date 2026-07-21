package model

// Translation is the deterministic output of query compatibility analysis.
type Translation struct {
	Kind        TranslationKind `json:"kind"`
	Builder     *BuilderQuery   `json:"builder,omitempty"`
	Formula     *Formula        `json:"formula,omitempty"`
	PromQL      string          `json:"promql,omitempty"`
	Decision    Decision        `json:"decision"`
	ParseErrors []ParseError    `json:"parseErrors,omitempty"`
	Legend      *string         `json:"legend,omitempty"`
}

// TranslationKind identifies the target query representation.
type TranslationKind string

const (
	TranslationBuilder TranslationKind = "builder"
	TranslationFormula TranslationKind = "formula"
	TranslationPromQL  TranslationKind = "promql"
	TranslationNone    TranslationKind = "none"
)

// BuilderQuery is a source-neutral description of a SigNoz metric query.
type BuilderQuery struct {
	Name             string     `json:"name"`
	MetricName       string     `json:"metricName"`
	Temporality      string     `json:"temporality,omitempty"`
	TimeAggregation  string     `json:"timeAggregation,omitempty"`
	SpaceAggregation string     `json:"spaceAggregation"`
	Filters          []Filter   `json:"filters,omitempty"`
	GroupBy          []string   `json:"groupBy,omitempty"`
	Functions        []Function `json:"functions,omitempty"`
	StepSeconds      int        `json:"stepSeconds,omitempty"`
}

// Filter is a normalized label matcher.
type Filter struct {
	Label    string `json:"label"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// Function describes a post-aggregation query function.
type Function struct {
	Name string    `json:"name"`
	Args []float64 `json:"args,omitempty"`
}

// Formula combines named Builder queries.
type Formula struct {
	Name       string         `json:"name"`
	Expression string         `json:"expression"`
	Queries    []BuilderQuery `json:"queries"`
}

// ParseError records a PromQL parser error without exposing parser internals.
type ParseError struct {
	Message string `json:"message"`
	Start   int    `json:"start,omitempty"`
	End     int    `json:"end,omitempty"`
}

// TargetMetric is live SigNoz metadata used to qualify Builder candidates.
type TargetMetric struct {
	Name        string   `json:"name,omitempty"`
	Type        string   `json:"type"`
	Temporality string   `json:"temporality"`
	IsMonotonic bool     `json:"isMonotonic"`
	Attributes  []string `json:"attributes,omitempty"`
}
