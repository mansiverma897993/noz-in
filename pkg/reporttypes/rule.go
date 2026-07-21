package reporttypes

// RuleReport is the machine-readable Prometheus rule migration evidence.
type RuleReport struct {
	SchemaVersion   string              `json:"schemaVersion"`
	Tool            Tool                `json:"tool"`
	Run             Run                 `json:"run"`
	ArtifactSet     *ArtifactSetBinding `json:"artifactSet,omitempty"`
	PrimaryArtifact *ArtifactBinding    `json:"primaryArtifact,omitempty"`
	Source          Source              `json:"source"`
	Summary         RuleSummary         `json:"summary"`
	ReasonCodes     map[string]string   `json:"reasonCodeIndex"`
	Groups          []RuleGroupRecord   `json:"groups"`
}

// RuleSummary accounts for every alerting and recording rule.
type RuleSummary struct {
	Groups             int `json:"groups"`
	Rules              int `json:"rules"`
	Alerting           int `json:"alerting"`
	Recording          int `json:"recording"`
	Emitted            int `json:"emitted"`
	Enabled            int `json:"enabled"`
	Disabled           int `json:"disabled"`
	Passthrough        int `json:"passthrough"`
	NeedsReview        int `json:"needsReview"`
	Previewed          int `json:"previewed"`
	PreviewValid       int `json:"previewValid"`
	PreviewInvalid     int `json:"previewInvalid"`
	Executed           int `json:"executed"`
	DataPresent        int `json:"dataPresent"`
	DataAbsent         int `json:"dataAbsent"`
	Created            int `json:"created"`
	Updated            int `json:"updated"`
	NotCreatedDisabled int `json:"notCreatedDisabled"`
}

// RuleGroupRecord preserves source evaluation grouping.
type RuleGroupRecord struct {
	Name        string            `json:"name"`
	Interval    string            `json:"interval,omitempty"`
	QueryOffset string            `json:"queryOffset,omitempty"`
	Limit       int               `json:"limit"`
	Labels      map[string]string `json:"labels,omitempty"`
	SourcePath  string            `json:"sourcePath"`
	Rules       []RuleRecord      `json:"rules"`
}

// RuleRecord explains one source rule and its target outcome.
type RuleRecord struct {
	SourcePath         string            `json:"sourcePath"`
	Alert              string            `json:"alert,omitempty"`
	Record             string            `json:"record,omitempty"`
	Original           string            `json:"original"`
	For                string            `json:"for,omitempty"`
	KeepFiringFor      string            `json:"keepFiringFor,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	Annotations        map[string]string `json:"annotations,omitempty"`
	Verdict            string            `json:"verdict"`
	ReasonCodes        []string          `json:"reasonCodes,omitempty"`
	Notes              []string          `json:"notes,omitempty"`
	TargetAlert        string            `json:"targetAlert,omitempty"`
	TargetMigrationID  string            `json:"targetMigrationId,omitempty"`
	EmittedSpecSHA256  string            `json:"emittedSpecSha256,omitempty"`
	PromQL             string            `json:"promql,omitempty"`
	ExtractedThreshold bool              `json:"extractedThreshold"`
	Operator           string            `json:"operator,omitempty"`
	Target             float64           `json:"target,omitempty"`
	EvalWindow         string            `json:"evalWindow,omitempty"`
	Frequency          string            `json:"frequency,omitempty"`
	RequireMinPoints   bool              `json:"requireMinPoints,omitempty"`
	RequiredNumPoints  int               `json:"requiredNumPoints,omitempty"`
	Disabled           bool              `json:"disabled,omitempty"`
	Validation         Validation        `json:"validation"`
	Write              *RuleWriteRecord  `json:"write,omitempty"`
}

// RuleWriteRecord records the exact target disposition of one emitted rule.
type RuleWriteRecord struct {
	Requested bool   `json:"requested"`
	Attempted bool   `json:"attempted"`
	Succeeded bool   `json:"succeeded"`
	ID        string `json:"id,omitempty"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
}
