package reporttypes

import "encoding/json"

// Report is the machine-readable migration evidence contract.
type Report struct {
	SchemaVersion   string                `json:"schemaVersion"`
	Tool            Tool                  `json:"tool"`
	Run             Run                   `json:"run"`
	ArtifactSet     *ArtifactSetBinding   `json:"artifactSet,omitempty"`
	PrimaryArtifact *ArtifactBinding      `json:"primaryArtifact,omitempty"`
	Differential    *DifferentialEvidence `json:"differential,omitempty"`
	Source          Source                `json:"source"`
	SourceInventory SourceInventory       `json:"sourceInventory"`
	Dashboard       DashboardInfo         `json:"dashboard"`
	Summary         Summary               `json:"summary"`
	ReasonCodes     map[string]string     `json:"reasonCodeIndex"`
	Panels          []PanelRecord         `json:"panels"`
	Variables       []VariableRecord      `json:"variables,omitempty"`
	SourceFeatures  []SourceFeatureRecord `json:"sourceFeatures,omitempty"`
}

// ArtifactSetBinding identifies the commit manifest for a complete artifact
// generation. The manifest is published after every member and binds the exact
// report, primary payload, HTML rendering, and optional candidate payload.
type ArtifactSetBinding struct {
	Path       string `json:"path"`
	Generation string `json:"generation"`
}

// ArtifactBinding identifies the exact primary dashboard file covered by a
// migration report. Path is a portable filename relative to the report.
type ArtifactBinding struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

// DifferentialEvidence records the run-level provenance for query comparison
// records attached to a migration report.
type DifferentialEvidence struct {
	SourceURL string `json:"sourceUrl"`
	TargetURL string `json:"targetUrl"`
	// TargetProvenance is an operator-supplied ingestion-path assertion. An
	// empty value means exact matching permitted no target-only labels.
	TargetProvenance string                      `json:"targetProvenance"`
	PrimaryArtifact  ArtifactBinding             `json:"primaryArtifact"`
	Materialization  DifferentialMaterialization `json:"materialization"`
	Window           DifferentialWindow          `json:"window"`
	Tolerances       DifferentialTolerances      `json:"tolerances"`
	Summary          DifferentialSummary         `json:"summary"`
}

// DifferentialMaterialization records the migration-time Grafana macro
// settings used to construct the source side of a comparison.
type DifferentialMaterialization struct {
	RateInterval string `json:"rateInterval"`
	Interval     string `json:"interval"`
	Range        string `json:"range"`
}

// DifferentialWindow is the overall aligned time window requested by a run.
type DifferentialWindow struct {
	Start      string `json:"start"`
	End        string `json:"end"`
	StepMillis int64  `json:"stepMillis"`
}

// DifferentialTolerances records every comparator acceptance threshold.
type DifferentialTolerances struct {
	TimestampMillis      int64   `json:"timestampMillis"`
	Relative             float64 `json:"relative"`
	Absolute             float64 `json:"absolute"`
	Coverage             float64 `json:"coverage"`
	MinimumMatchedPoints int     `json:"minimumMatchedPoints"`
}

// DifferentialSummary counts every comparison outcome retained in evidence.
type DifferentialSummary struct {
	Queries             int `json:"queries"`
	Compared            int `json:"compared"`
	Equivalent          int `json:"equivalent"`
	ValueMismatch       int `json:"valueMismatch"`
	InsufficientOverlap int `json:"insufficientOverlap"`
	NoSourceData        int `json:"noSourceData"`
	NoTargetData        int `json:"noTargetData"`
	BothEmpty           int `json:"bothEmpty"`
	TargetOnlyData      int `json:"targetOnlyData"`
	NoSeriesMatch       int `json:"noSeriesMatch"`
	Errors              int `json:"errors"`
	Skipped             int `json:"skipped"`
}

// Tool identifies the binary that produced an evidence artifact.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Run records the live context without storing credentials.
type Run struct {
	StartedAt     string         `json:"startedAt,omitempty"`
	Target        string         `json:"target,omitempty"`
	SigNozVersion string         `json:"signozVersion,omitempty"`
	Flags         map[string]any `json:"flags,omitempty"`
}

// DashboardInfo carries source identity without overloading the display title.
type DashboardInfo struct {
	Title         string `json:"title"`
	GrafanaUID    string `json:"grafanaUid,omitempty"`
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	Source        string `json:"source,omitempty"`
}

// Source identifies the input artifact summarized by a report.
type Source struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	Path          string `json:"path,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Identity      string `json:"identity,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
}

// SourceInventory records objects counted before source normalization.
type SourceInventory struct {
	Panels         int `json:"panels"`
	Queries        int `json:"queries"`
	Variables      int `json:"variables"`
	SourceFeatures int `json:"sourceFeatures"`
}

// Summary aggregates effective emitted outcomes.
type Summary struct {
	Panels                    int     `json:"panels"`
	Queries                   int     `json:"queries"`
	Native                    int     `json:"native"`
	Builder                   int     `json:"builder"`
	Formula                   int     `json:"formula"`
	Passthrough               int     `json:"passthrough"`
	NeedsReview               int     `json:"needsReview"`
	BuilderPanels             int     `json:"builderPanels"`
	PromQLPanels              int     `json:"promqlPanels"`
	PanelsAccounted           int     `json:"panelsAccounted"`
	QueriesAccounted          int     `json:"queriesAccounted"`
	VariablesAccounted        int     `json:"variablesAccounted"`
	SourceFeaturesAccounted   int     `json:"sourceFeaturesAccounted"`
	ReconciliationComplete    bool    `json:"reconciliationComplete"`
	PanelsNative              int     `json:"panelsNative"`
	PanelsPassthrough         int     `json:"panelsPassthrough"`
	PanelsNeedsReview         int     `json:"panelsNeedsReview"`
	PanelsOmitted             int     `json:"panelsOmitted"`
	Variables                 int     `json:"variables"`
	VariablesNeedsReview      int     `json:"variablesNeedsReview"`
	SourceFeatures            int     `json:"sourceFeatures"`
	SourceFeaturesNeedsReview int     `json:"sourceFeaturesNeedsReview"`
	Previewed                 int     `json:"previewed"`
	PreviewValid              int     `json:"previewValid"`
	PreviewInvalid            int     `json:"previewInvalid"`
	ValidationEligible        int     `json:"validationEligible"`
	ValidationFailed          int     `json:"validationFailed"`
	Executed                  int     `json:"executed"`
	DataPresent               int     `json:"dataPresent"`
	DataAbsent                int     `json:"dataAbsent"`
	DataPresentPercent        float64 `json:"dataPresentPercent"`
	Headline                  string  `json:"headline"`
}

// PanelRecord describes one source panel and its target mode.
type PanelRecord struct {
	ID              string                `json:"id"`
	EmittedWidgetID string                `json:"emittedWidgetId,omitempty"`
	PrimaryArtifact bool                  `json:"primaryArtifact"`
	Title           string                `json:"title"`
	Kind            string                `json:"kind"`
	SourceType      string                `json:"sourceType,omitempty"`
	EmittedKind     string                `json:"emittedKind"`
	SourcePath      string                `json:"sourcePath"`
	EmittedMode     string                `json:"emittedMode"`
	Verdict         string                `json:"verdict"`
	State           string                `json:"state"`
	ReasonCodes     []string              `json:"reasonCodes,omitempty"`
	Content         string                `json:"content,omitempty"`
	Transforms      []string              `json:"transforms,omitempty"`
	Repeat          string                `json:"repeat,omitempty"`
	TimeFrom        string                `json:"timeFrom,omitempty"`
	TimeShift       string                `json:"timeShift,omitempty"`
	Queries         []QueryRecord         `json:"queries"`
	SourceFeatures  []SourceFeatureRecord `json:"sourceFeatures,omitempty"`
}

// SourceFeatureRecord accounts for a source construct with no target representation.
type SourceFeatureRecord struct {
	Kind       string `json:"kind"`
	SourcePath string `json:"sourcePath"`
	Detail     string `json:"detail,omitempty"`
	Verdict    string `json:"verdict"`
	ReasonCode string `json:"reasonCode"`
}

// VariableRecord explains how one source variable is represented in SigNoz.
type VariableRecord struct {
	Name           string                `json:"name"`
	Label          string                `json:"label,omitempty"`
	SourcePath     string                `json:"sourcePath"`
	SourceKind     string                `json:"sourceKind"`
	EmittedKind    string                `json:"emittedKind"`
	Attribute      string                `json:"attribute,omitempty"`
	Current        []string              `json:"current,omitempty"`
	AllValue       string                `json:"allValue,omitempty"`
	Verdict        string                `json:"verdict"`
	ReasonCodes    []string              `json:"reasonCodes,omitempty"`
	Notes          []string              `json:"notes,omitempty"`
	SourceFeatures []SourceFeatureRecord `json:"sourceFeatures,omitempty"`
}

// QueryRecord describes candidate and effective treatment of one source query.
type QueryRecord struct {
	RefID             string                `json:"refId"`
	OriginalRefID     string                `json:"originalRefId,omitempty"`
	SourcePath        string                `json:"sourcePath"`
	Original          string                `json:"original"`
	OriginalLegend    string                `json:"originalLegend,omitempty"`
	EmittedLegend     string                `json:"emittedLegend,omitempty"`
	Disabled          bool                  `json:"disabled,omitempty"`
	Instant           bool                  `json:"instant,omitempty"`
	Format            string                `json:"format,omitempty"`
	Step              int                   `json:"step,omitempty"`
	Interval          string                `json:"interval,omitempty"`
	IntervalFactor    int                   `json:"intervalFactor,omitempty"`
	MaxDataPoints     int                   `json:"maxDataPoints,omitempty"`
	CandidateKind     string                `json:"candidateKind"`
	EmittedKind       string                `json:"emittedKind"`
	EmittedQueryName  string                `json:"emittedQueryName,omitempty"`
	EmittedExpression string                `json:"emittedExpression,omitempty"`
	EmittedSpecSHA256 string                `json:"emittedSpecSha256,omitempty"`
	Verdict           string                `json:"verdict"`
	ReasonCodes       []string              `json:"reasonCodes,omitempty"`
	Builder           *BuilderQuery         `json:"builder,omitempty"`
	Formula           *Formula              `json:"formula,omitempty"`
	PromQL            string                `json:"promql,omitempty"`
	ParseErrors       []ParseError          `json:"parseErrors,omitempty"`
	Notes             []string              `json:"notes,omitempty"`
	SourceFeatures    []SourceFeatureRecord `json:"sourceFeatures,omitempty"`
	Validation        Validation            `json:"validation"`
	Comparison        json.RawMessage       `json:"comparison,omitempty"`
}

// BuilderQuery is the report representation of a structurally valid Builder candidate.
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

// Formula is the report representation of a structurally valid Builder formula candidate.
type Formula struct {
	Name       string         `json:"name"`
	Expression string         `json:"expression"`
	Queries    []BuilderQuery `json:"queries"`
}

// Filter is a label constraint in a Builder query candidate.
type Filter struct {
	Label    string `json:"label"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// Function is a post-aggregation function in a Builder query candidate.
type Function struct {
	Name string    `json:"name"`
	Args []float64 `json:"args,omitempty"`
}

// ParseError records a canonical PromQL parser failure.
type ParseError struct {
	Message string `json:"message"`
	Start   int    `json:"start,omitempty"`
	End     int    `json:"end,omitempty"`
}

// Validation records target-side evidence added by live validation.
type Validation struct {
	Previewed         bool              `json:"previewed"`
	PreviewOK         bool              `json:"previewOk"`
	MetricFound       bool              `json:"metricFound"`
	MetricChecked     bool              `json:"metricChecked"`
	Executed          bool              `json:"executed"`
	DataPresent       bool              `json:"dataPresent"`
	Series            int               `json:"series,omitempty"`
	Points            int               `json:"points,omitempty"`
	Rows              int               `json:"rows,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
	Error             string            `json:"error,omitempty"`
	HTTPStatus        int               `json:"httpStatus,omitempty"`
	MissingVariables  []string          `json:"missingVariables,omitempty"`
	ReasonCodes       []string          `json:"reasonCodes,omitempty"`
	PreviewStatements []json.RawMessage `json:"previewStatements,omitempty"`
	PreviewWarnings   []json.RawMessage `json:"previewWarnings,omitempty"`
	CheckedAt         string            `json:"checkedAt,omitempty"`
}
