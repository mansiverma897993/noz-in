package model

// Dashboard is the source-neutral representation of a dashboard.
type Dashboard struct {
	Title           string            `json:"title"`
	Description     string            `json:"description,omitempty"`
	UID             string            `json:"uid,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Source          Source            `json:"source"`
	Panels          []Panel           `json:"panels"`
	Variables       []Variable        `json:"variables,omitempty"`
	InputBindings   map[string]string `json:"inputBindings,omitempty"`
	SourceFeatures  []SourceFeature   `json:"sourceFeatures,omitempty"`
	SourceInventory SourceInventory   `json:"sourceInventory,omitzero"`
}

// SourceInventory is captured from the decoded source before normalization so
// evidence can reconcile source objects against emitted or reviewed records.
type SourceInventory struct {
	Captured       bool `json:"captured"`
	Panels         int  `json:"panels"`
	Queries        int  `json:"queries"`
	Variables      int  `json:"variables"`
	SourceFeatures int  `json:"sourceFeatures"`
}

// Source describes the input artifact that produced a model object.
type Source struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	Path          string `json:"path,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Identity      string `json:"identity,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
}

// Panel is a normalized dashboard panel.
type Panel struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Kind           PanelKind       `json:"kind"`
	SourceType     string          `json:"sourceType,omitempty"`
	Description    string          `json:"description,omitempty"`
	Content        string          `json:"content,omitempty"`
	Unit           string          `json:"unit,omitempty"`
	Grid           Grid            `json:"grid"`
	Datasource     Datasource      `json:"datasource,omitzero"`
	Queries        []Query         `json:"queries"`
	Transforms     []string        `json:"transforms,omitempty"`
	Repeat         string          `json:"repeat,omitempty"`
	TimeFrom       string          `json:"timeFrom,omitempty"`
	TimeShift      string          `json:"timeShift,omitempty"`
	Collapsed      bool            `json:"collapsed,omitempty"`
	SourcePath     string          `json:"sourcePath"`
	SourceFeatures []SourceFeature `json:"sourceFeatures,omitempty"`
}

// Grid describes a panel position in Grafana's 24-column coordinate space.
type Grid struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// PanelKind is a normalized visualization kind.
type PanelKind string

const (
	PanelKindGraph     PanelKind = "graph"
	PanelKindValue     PanelKind = "value"
	PanelKindBar       PanelKind = "bar"
	PanelKindTable     PanelKind = "table"
	PanelKindPie       PanelKind = "pie"
	PanelKindHistogram PanelKind = "histogram"
	PanelKindRow       PanelKind = "row"
	PanelKindText      PanelKind = "text"
	PanelKindUnknown   PanelKind = "unknown"
)

// Datasource identifies a query backend without retaining Grafana wire types.
type Datasource struct {
	Type string `json:"type,omitempty"`
	UID  string `json:"uid,omitempty"`
	Name string `json:"name,omitempty"`
}

// Query is a normalized source query.
type Query struct {
	RefID           string          `json:"refId"`
	Expression      string          `json:"expression,omitempty"`
	Legend          string          `json:"legend,omitempty"`
	Hidden          bool            `json:"hidden,omitempty"`
	Instant         bool            `json:"instant,omitempty"`
	Format          string          `json:"format,omitempty"`
	QueryType       string          `json:"queryType,omitempty"`
	Step            int             `json:"step,omitempty"`
	Interval        string          `json:"interval,omitempty"`
	IntervalFactor  int             `json:"intervalFactor,omitempty"`
	MaxDataPoints   int             `json:"maxDataPoints,omitempty"`
	Datasource      Datasource      `json:"datasource,omitzero"`
	SourcePath      string          `json:"sourcePath"`
	OriginalRefID   string          `json:"originalRefId,omitempty"`
	RefIDNormalized bool            `json:"refIdNormalized,omitempty"`
	SourceFeatures  []SourceFeature `json:"sourceFeatures,omitempty"`
}

// SourceFeature records a source construct that cannot be emitted without loss.
type SourceFeature struct {
	Kind       string     `json:"kind"`
	SourcePath string     `json:"sourcePath"`
	Detail     string     `json:"detail,omitempty"`
	Reason     ReasonCode `json:"reasonCode"`
}

// Variable is a normalized dashboard variable.
type Variable struct {
	Name           string          `json:"name"`
	Label          string          `json:"label,omitempty"`
	Kind           VariableKind    `json:"kind"`
	Query          string          `json:"query,omitempty"`
	Regex          string          `json:"regex,omitempty"`
	Current        []string        `json:"current,omitempty"`
	Multi          bool            `json:"multi,omitempty"`
	IncludeAll     bool            `json:"includeAll,omitempty"`
	AllValue       string          `json:"allValue,omitempty"`
	Datasource     Datasource      `json:"datasource,omitzero"`
	SourcePath     string          `json:"sourcePath"`
	SourceFeatures []SourceFeature `json:"sourceFeatures,omitempty"`
}

// VariableKind is a normalized variable type.
type VariableKind string

const (
	VariableKindQuery      VariableKind = "query"
	VariableKindCustom     VariableKind = "custom"
	VariableKindInterval   VariableKind = "interval"
	VariableKindConstant   VariableKind = "constant"
	VariableKindDatasource VariableKind = "datasource"
	VariableKindText       VariableKind = "textbox"
	VariableKindUnknown    VariableKind = "unknown"
)
