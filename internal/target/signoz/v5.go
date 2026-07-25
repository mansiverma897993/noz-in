package signoz

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/stableidentity"
)

// DashboardV5 is the SigNoz dashboard import payload.
type DashboardV5 struct {
	Title           string                `json:"title"`
	Description     string                `json:"description"`
	Tags            []string              `json:"tags"`
	Version         string                `json:"version"`
	Layout          []Layout              `json:"layout"`
	PanelMap        map[string]PanelGroup `json:"panelMap"`
	Widgets         []Widget              `json:"widgets"`
	Variables       map[string]VariableV5 `json:"variables"`
	UploadedGrafana bool                  `json:"uploadedGrafana"`
	UUID            string                `json:"uuid"`
}

// PanelGroup preserves SigNoz row containment and collapse state. Child
// widgets remain in the top-level widgets collection. Their layouts always
// live here and are also present in the dashboard layout while the row is
// expanded, matching SigNoz's row-toggle contract.
type PanelGroup struct {
	Collapsed bool     `json:"collapsed"`
	Widgets   []Layout `json:"widgets"`
}

// Layout positions a widget on the SigNoz grid.
type Layout struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	I      string `json:"i"`
	Moved  bool   `json:"moved"`
	Static bool   `json:"static"`
}

// Widget is a SigNoz v5 dashboard widget.
type Widget struct {
	SourcePath        string      `json:"-"`
	Description       string      `json:"description"`
	FillSpans         bool        `json:"fillSpans"`
	SpanGaps          bool        `json:"spanGaps"`
	LineInterpolation string      `json:"lineInterpolation"`
	ShowPoints        bool        `json:"showPoints"`
	ID                string      `json:"id"`
	IsStacked         bool        `json:"isStacked"`
	NullZeroValues    string      `json:"nullZeroValues"`
	Opacity           string      `json:"opacity"`
	PanelTypes        string      `json:"panelTypes"`
	Query             WidgetQuery `json:"query"`
	SoftMax           *float64    `json:"softMax"`
	SoftMin           *float64    `json:"softMin"`
	Thresholds        []any       `json:"thresholds"`
	TimePreference    string      `json:"timePreferance"`
	Title             string      `json:"title"`
	YAxisUnit         string      `json:"yAxisUnit"`
}

// WidgetQuery contains each v5 query representation and selects one.
type WidgetQuery struct {
	Builder       BuilderContainer `json:"builder"`
	ClickHouseSQL []PromQLQuery    `json:"clickhouse_sql"`
	ID            string           `json:"id"`
	PromQL        []PromQLQuery    `json:"promql"`
	QueryType     string           `json:"queryType"`
}

// BuilderContainer contains metric queries and formulas.
type BuilderContainer struct {
	QueryData     []BuilderQueryData `json:"queryData"`
	QueryFormulas []BuilderFormula   `json:"queryFormulas"`
}

// BuilderQueryData is the legacy dashboard form of a v5 Builder query.
type BuilderQueryData struct {
	DataSource   string              `json:"dataSource"`
	Disabled     bool                `json:"disabled"`
	Expression   string              `json:"expression"`
	Functions    []Function          `json:"functions"`
	GroupBy      []DashboardGroupBy  `json:"groupBy"`
	Having       Expression          `json:"having"`
	Legend       string              `json:"legend"`
	Limit        *int                `json:"limit"`
	OrderBy      []OrderBy           `json:"orderBy"`
	QueryName    string              `json:"queryName"`
	StepInterval int                 `json:"stepInterval"`
	Aggregations []MetricAggregation `json:"aggregations"`
	Filter       Expression          `json:"filter"`
}

// MetricAggregation describes a SigNoz metric aggregation.
type MetricAggregation struct {
	MetricName       string  `json:"metricName"`
	Temporality      *string `json:"temporality"`
	TimeAggregation  string  `json:"timeAggregation,omitempty"`
	SpaceAggregation string  `json:"spaceAggregation"`
	ReduceTo         string  `json:"reduceTo,omitempty"`
}

// DashboardGroupBy is the legacy field descriptor persisted in dashboards.
// The SigNoz frontend converts this shape to the v5 GroupBy request shape.
type DashboardGroupBy struct {
	Key      string `json:"key"`
	DataType string `json:"dataType,omitempty"`
	Type     string `json:"type,omitempty"`
}

// GroupBy identifies a metric field used for grouping in a v5 query request.
type GroupBy struct {
	Name          string `json:"name"`
	FieldDataType string `json:"fieldDataType,omitempty"`
	FieldContext  string `json:"fieldContext,omitempty"`
}

// Function is a v5 query function.
type Function struct {
	Name string        `json:"name"`
	Args []FunctionArg `json:"args"`
}

// FunctionArg is a numeric function argument.
type FunctionArg struct {
	Value float64 `json:"value"`
}

// OrderBy controls target ordering.
type OrderBy struct {
	ColumnName string `json:"columnName"`
	Order      string `json:"order"`
}

// Expression wraps a FilterQuery or HavingExpression string.
type Expression struct {
	Expression string `json:"expression"`
}

// BuilderFormula is the legacy dashboard form of a formula.
type BuilderFormula struct {
	Disabled   bool   `json:"disabled"`
	Expression string `json:"expression"`
	Legend     string `json:"legend"`
	QueryName  string `json:"queryName"`
}

// PromQLQuery is a v5 PromQL or ClickHouse stub.
type PromQLQuery struct {
	Disabled bool   `json:"disabled"`
	Legend   string `json:"legend"`
	Name     string `json:"name"`
	Query    string `json:"query"`
}

// VariableV5 is a SigNoz dashboard variable.
type VariableV5 struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	Description               string `json:"description"`
	Type                      string `json:"type"`
	Order                     int    `json:"order"`
	Sort                      string `json:"sort"`
	MultiSelect               bool   `json:"multiSelect"`
	ShowAllOption             bool   `json:"showALLOption"`
	CustomValue               string `json:"customValue"`
	TextboxValue              string `json:"textboxValue"`
	SelectedValue             any    `json:"selectedValue,omitempty"`
	DefaultValue              string `json:"defaultValue,omitempty"`
	AllSelected               bool   `json:"allSelected,omitempty"`
	DynamicVariablesAttribute string `json:"dynamicVariablesAttribute,omitempty"`
	DynamicVariablesSource    string `json:"dynamicVariablesSource,omitempty"`
}

// EmitV5 produces a deterministic SigNoz dashboard payload.
func EmitV5(migration model.Migration) DashboardV5 {
	dashboard := DashboardV5{
		Title:           migration.Dashboard.Title,
		Description:     migration.Dashboard.Description,
		Tags:            append([]string(nil), migration.Dashboard.Tags...),
		Version:         "v5",
		Variables:       emitVariables(migration),
		PanelMap:        make(map[string]PanelGroup),
		UploadedGrafana: true,
		UUID:            DashboardUUID(migration.Dashboard),
	}
	if dashboard.Tags == nil {
		dashboard.Tags = []string{}
	}
	rowIDs := make(map[string]rowPlacement)
	for _, panel := range migration.Dashboard.Panels {
		if panel.Kind == model.PanelKindRow && migration.PanelEmittable(panel) {
			rowIDs[panel.SourcePath] = rowPlacement{
				id:        stableID(migration.Dashboard.UID, panel.SourcePath),
				collapsed: panel.Collapsed,
			}
		}
	}
	rowAssociation := associateRows(migration.Dashboard.Panels, rowIDs)
	for _, panel := range migration.Dashboard.Panels {
		if !migration.PanelEmittable(panel) {
			continue
		}
		widgetID := stableID(migration.Dashboard.UID, panel.SourcePath)
		candidate := emitLayout(panel.Grid, widgetID)
		if panel.Kind == model.PanelKindRow {
			layout := placeLayout(candidate, dashboard.Layout)
			dashboard.Layout = append(dashboard.Layout, layout)
			dashboard.PanelMap[widgetID] = PanelGroup{Collapsed: panel.Collapsed, Widgets: []Layout{}}
		} else if row, found := rowAssociation[panel.SourcePath]; found {
			group := dashboard.PanelMap[row.id]
			var layout Layout
			if row.collapsed {
				layout = placeLayout(candidate, group.Widgets)
			} else {
				layout = placeLayout(candidate, dashboard.Layout)
				dashboard.Layout = append(dashboard.Layout, layout)
			}
			group.Widgets = append(group.Widgets, layout)
			dashboard.PanelMap[row.id] = group
		} else {
			layout := placeLayout(candidate, dashboard.Layout)
			dashboard.Layout = append(dashboard.Layout, layout)
		}
		dashboard.Widgets = append(dashboard.Widgets, emitWidget(migration, panel, widgetID))
	}
	return dashboard
}

type rowPlacement struct {
	id        string
	collapsed bool
}

func containingRow(panelPath string, rows map[string]rowPlacement) (rowPlacement, bool) {
	bestPath := ""
	var best rowPlacement
	for rowPath, row := range rows {
		if strings.HasPrefix(panelPath, rowPath+"/panels/") && len(rowPath) > len(bestPath) {
			bestPath = rowPath
			best = row
		}
	}
	return best, bestPath != ""
}

// associateRows maps each non-row panel to the row that visually contains it.
// A collapsed row stores its children nested under its own path (handled by
// containingRow). A Grafana schemaVersion >= 16 *expanded* row carries an empty
// panels[] and its children are the top-level siblings that follow it in
// document order until the next row; those are reassociated here so collapsing
// the row in SigNoz hides them and the row is not emitted as an empty group.
func associateRows(panels []model.Panel, rows map[string]rowPlacement) map[string]rowPlacement {
	association := make(map[string]rowPlacement)
	for _, panel := range panels {
		if panel.Kind == model.PanelKindRow {
			continue
		}
		if row, found := containingRow(panel.SourcePath, rows); found {
			association[panel.SourcePath] = row
		}
	}
	var current *rowPlacement
	for index := range panels {
		panel := panels[index]
		if panel.Kind == model.PanelKindRow {
			if row, ok := rows[panel.SourcePath]; ok && !row.collapsed {
				placement := row
				current = &placement
			} else {
				current = nil
			}
			continue
		}
		if current == nil || !isTopLevelPanelPath(panel.SourcePath) {
			continue
		}
		if _, already := association[panel.SourcePath]; !already {
			association[panel.SourcePath] = *current
		}
	}
	return association
}

// isTopLevelPanelPath reports whether a panel source path is a direct child of
// the dashboard's top-level panels[] array (/panels/N). It is false for panels
// nested inside a collapsed row (/panels/N/panels/M) and for the sv14 rows[]
// scheme (/rows/N/panels/M), both of which are row-scoped by their own path.
func isTopLevelPanelPath(panelPath string) bool {
	const prefix = "/panels/"
	if !strings.HasPrefix(panelPath, prefix) {
		return false
	}
	return !strings.Contains(panelPath[len(prefix):], "/")
}

func emitLayout(grid model.Grid, id string) Layout {
	x := min(max((grid.X+1)/2, 0), 11)
	right := min(max((grid.X+grid.W+1)/2, x+1), 12)
	return Layout{
		X: x,
		Y: grid.Y,
		W: max(right-x, 1),
		// Grafana grid rows are 30px and SigNoz grid rows are ~45px, so the
		// emitted height is rounded to two thirds to preserve the source
		// dashboard's visual density.
		H:      max((grid.H*2+1)/3, 1),
		I:      id,
		Moved:  false,
		Static: false,
	}
}

func placeLayout(candidate Layout, placed []Layout) Layout {
	for {
		shift := candidate.X
		bottom := candidate.Y
		collision := false
		for _, existing := range placed {
			if !layoutsOverlap(candidate, existing) {
				continue
			}
			collision = true
			shift = max(shift, existing.X+existing.W)
			bottom = max(bottom, existing.Y+existing.H)
		}
		if !collision {
			return candidate
		}
		if shift+candidate.W <= 12 {
			candidate.X = shift
			continue
		}
		candidate.X = 0
		candidate.Y = bottom
	}
}

func layoutsOverlap(left, right Layout) bool {
	return left.X < right.X+right.W && right.X < left.X+left.W &&
		left.Y < right.Y+right.H && right.Y < left.Y+left.H
}

func emitWidget(migration model.Migration, panel model.Panel, id string) Widget {
	query := emitWidgetQuery(migration, panel, id)
	return Widget{
		SourcePath:        panel.SourcePath,
		Description:       panel.Description,
		FillSpans:         false,
		SpanGaps:          false,
		LineInterpolation: "linear",
		ShowPoints:        false,
		ID:                id,
		IsStacked:         false,
		NullZeroValues:    "zero",
		Opacity:           "1",
		PanelTypes:        emittedPanelType(migration, panel),
		Query:             query,
		SoftMax:           nil,
		SoftMin:           nil,
		Thresholds:        []any{},
		TimePreference:    "GLOBAL_TIME",
		Title:             panel.Title,
		YAxisUnit:         defaultUnit(panel.Unit),
	}
}

func emittedPanelType(migration model.Migration, panel model.Panel) string {
	return EmittedPanelType(panel.Kind, migration.PanelMode(panel))
}

// EmittedPanelType reports the SigNoz visualization a source panel of this kind
// and translation mode is emitted as. Evidence reporting calls it so a report
// can never describe a visualization the emitter did not actually write.
func EmittedPanelType(kind model.PanelKind, mode model.TranslationKind) string {
	if kind == model.PanelKindHistogram || kind == model.PanelKindBar {
		return "graph"
	}
	// Pinned SigNoz v0.133 reduces a PromQL value/stat response through a
	// scalar path that can surface the first series' oldest point instead of
	// Grafana's last value, and table/pie share that reduction. Verified live:
	// a CPU-busy series returning 85.79 … 9.79 renders as 85.79. A misleading
	// number is worse than an honest graph, so these stay graphs.
	if mode == model.TranslationPromQL &&
		(kind == model.PanelKindValue || kind == model.PanelKindTable || kind == model.PanelKindPie) {
		return "graph"
	}
	return panelType(kind)
}

func emitWidgetQuery(migration model.Migration, panel model.Panel, id string) WidgetQuery {
	reduceTo := scalarReduction(panel.Kind)
	result := WidgetQuery{
		Builder:       BuilderContainer{QueryData: []BuilderQueryData{}, QueryFormulas: []BuilderFormula{}},
		ClickHouseSQL: []PromQLQuery{emptyQuery("A")},
		ID:            stableID(id, "query"),
		PromQL:        []PromQLQuery{emptyQuery("A")},
		QueryType:     "builder",
	}
	if len(panel.Queries) == 0 {
		return result
	}

	if migration.PanelMode(panel) == model.TranslationBuilder {
		for _, query := range panel.Queries {
			translation, _ := migration.TranslationFor(query)
			emittedQuery := query
			emittedQuery.Legend = emittedLegend(translation, query.Legend)
			switch translation.Kind {
			case model.TranslationBuilder:
				result.Builder.QueryData = append(result.Builder.QueryData, emitBuilder(*translation.Builder, emittedQuery, reduceTo))
			case model.TranslationFormula:
				for _, formulaQuery := range translation.Formula.Queries {
					// Formula dependencies must execute but must not render as
					// independent source-visible series. Pinned SigNoz evaluates
					// disabled dependencies before filtering their results, which is
					// the canonical dashboard formula convention.
					result.Builder.QueryData = append(result.Builder.QueryData, emitBuilder(
						formulaQuery, model.Query{RefID: formulaQuery.Name, Hidden: true}, reduceTo,
					))
				}
				result.Builder.QueryFormulas = append(result.Builder.QueryFormulas, BuilderFormula{
					Disabled:   query.Hidden,
					Expression: translation.Formula.Expression,
					Legend:     emittedQuery.Legend,
					QueryName:  translation.Formula.Name,
				})
			}
		}
		return result
	}

	result.QueryType = "promql"
	result.PromQL = make([]PromQLQuery, 0, len(panel.Queries))
	for _, query := range panel.Queries {
		translation, ok := migration.TranslationFor(query)
		if !ok || translation.Kind == model.TranslationNone {
			continue
		}
		expression := query.Expression
		if ok && translation.PromQL != "" {
			expression = translation.PromQL
		}
		result.PromQL = append(result.PromQL, PromQLQuery{
			Disabled: query.Hidden || strings.TrimSpace(expression) == "",
			Legend:   emittedLegend(translation, query.Legend),
			Name:     defaultQueryName(query.RefID),
			Query:    expression,
		})
	}
	return result
}

func emittedLegend(translation model.Translation, fallback string) string {
	if translation.Legend != nil {
		return *translation.Legend
	}
	return fallback
}

func emitBuilder(builder model.BuilderQuery, query model.Query, reduceTo string) BuilderQueryData {
	groupBy := make([]DashboardGroupBy, 0, len(builder.GroupBy))
	for _, name := range builder.GroupBy {
		groupBy = append(groupBy, DashboardGroupBy{
			Key:      name,
			DataType: "string",
			Type:     dashboardFieldContext(name),
		})
	}
	functions := make([]Function, 0, len(builder.Functions))
	for _, function := range builder.Functions {
		args := make([]FunctionArg, 0, len(function.Args))
		for _, value := range function.Args {
			args = append(args, FunctionArg{Value: value})
		}
		functions = append(functions, Function{Name: function.Name, Args: args})
	}
	return BuilderQueryData{
		DataSource:   "metrics",
		Disabled:     query.Hidden,
		Expression:   builder.Name,
		Functions:    functions,
		GroupBy:      groupBy,
		Having:       Expression{},
		Legend:       query.Legend,
		Limit:        nil,
		OrderBy:      []OrderBy{},
		QueryName:    builder.Name,
		StepInterval: max(builder.StepSeconds, 60),
		Aggregations: []MetricAggregation{{
			MetricName:       builder.MetricName,
			Temporality:      optionalString(builder.Temporality),
			TimeAggregation:  builder.TimeAggregation,
			SpaceAggregation: builder.SpaceAggregation,
			ReduceTo:         reduceTo,
		}},
		Filter: Expression{Expression: filterExpression(builder.Filters)},
	}
}

func optionalString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func dashboardFieldContext(name string) string {
	switch name {
	case "service.name", "service.instance.id", "server.address", "server.port", "url.scheme":
		return "resource"
	default:
		return "tag"
	}
}

func scalarReduction(kind model.PanelKind) string {
	switch kind {
	case model.PanelKindValue, model.PanelKindTable, model.PanelKindPie:
		return "last"
	default:
		return ""
	}
}

func panelType(kind model.PanelKind) string {
	switch kind {
	case model.PanelKindGraph, model.PanelKindValue, model.PanelKindBar, model.PanelKindTable,
		model.PanelKindPie, model.PanelKindHistogram, model.PanelKindRow:
		return string(kind)
	default:
		return "graph"
	}
}

// DashboardUUID returns the exact stable target identity used by EmitV5.
// Batch preflight uses the same function so collision checks cannot drift from
// the emitted payload contract.
func DashboardUUID(dashboard model.Dashboard) string {
	uid := strings.TrimSpace(dashboard.UID)
	identity := strings.TrimSpace(dashboard.Source.Identity)
	if identity == "" {
		identity = strings.TrimSpace(dashboard.Source.Path)
	}
	namespace := strings.TrimSpace(dashboard.Source.Namespace)
	if namespace == "" {
		namespace = identity
	}
	if uid != "" {
		if namespace == "" {
			return stableID("grafana", uid)
		}
		return stableID("grafana", namespace, uid)
	}
	return stableID("grafana", namespace, identity)
}

func defaultUnit(unit string) string {
	if strings.TrimSpace(unit) == "" {
		return "none"
	}
	return unit
}

func filterExpression(filters []model.Filter) string {
	parts := make([]string, 0, len(filters))
	for _, filter := range filters {
		parts = append(parts, fmt.Sprintf("%s %s '%s'", filter.Label, filter.Operator, escapeFilterValue(filter.Value)))
	}
	return strings.Join(parts, " AND ")
}

func escapeFilterValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `'`, `\'`)
}

func emptyQuery(name string) PromQLQuery {
	return PromQLQuery{Name: name}
}

func emitVariables(migration model.Migration) map[string]VariableV5 {
	variables := make(map[string]VariableV5)
	for order, variable := range migration.Dashboard.Variables {
		translation, ok := migration.VariableTranslationFor(variable)
		if !ok || translation.Kind == "none" {
			continue
		}
		// Only DYNAMIC variables have a pinned SigNoz runtime representation
		// for All. CUSTOM/TEXTBOX/QUERY variables must carry their complete
		// selected scalar list; never persist a control sentinel or an
		// allSelected flag that getDashboardVariables will ignore.
		if variable.IncludeAll && isAllVariableValue(variable.Current) && translation.Kind != "dynamic" {
			continue
		}
		id := stableID(migration.Dashboard.UID, variable.SourcePath)
		emitted := VariableV5{
			ID:          id,
			Name:        variable.Name,
			Description: variable.Label,
			Order:       order,
			Sort:        "ASC",
			MultiSelect: variable.Multi || len(variable.Current) > 1 ||
				(translation.Kind == "dynamic" && variable.IncludeAll && isAllVariableValue(variable.Current)),
			ShowAllOption: variable.IncludeAll,
		}
		setVariableSelection(&emitted, variable)
		switch translation.Kind {
		case "dynamic":
			emitted.Type = "DYNAMIC"
			emitted.DynamicVariablesAttribute = targetLabel(translation.Attribute)
			emitted.DynamicVariablesSource = "Metrics"
		case "custom":
			customValue := translation.CustomValue
			if customValue == "" {
				var exact bool
				customValue, exact = EncodeStableCustomSelection(variable.Current)
				if !exact {
					continue
				}
			}
			selected, exact := DecodeStableCustomSelection(customValue)
			if !exact || !slices.Equal(selected, variable.Current) {
				continue
			}
			emitted.Type = "CUSTOM"
			emitted.CustomValue = customValue
		case "textbox":
			emitted.Type = "TEXTBOX"
			emitted.TextboxValue = firstValue(variable.Current)
		default:
			emitted.Type = "TEXTBOX"
			emitted.TextboxValue = firstValue(variable.Current)
		}
		variables[id] = emitted
	}
	return variables
}

func setVariableSelection(emitted *VariableV5, variable model.Variable) {
	if len(variable.Current) == 0 {
		return
	}
	if variable.IncludeAll && isAllVariableValue(variable.Current) {
		emitted.AllSelected = true
		return
	}
	if variable.Multi || len(variable.Current) > 1 {
		emitted.SelectedValue = append([]string(nil), variable.Current...)
		emitted.DefaultValue = variable.Current[0]
		return
	}
	emitted.SelectedValue = variable.Current[0]
	emitted.DefaultValue = variable.Current[0]
}

func isAllVariableValue(values []string) bool {
	if len(values) != 1 {
		return false
	}
	value := strings.TrimSpace(strings.ToLower(values[0]))
	return value == "all" || value == "$__all" || value == "__all__"
}

func targetLabel(label string) string {
	switch label {
	case "instance":
		return "service.instance.id"
	case "job":
		return "service.name"
	default:
		return label
	}
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func defaultQueryName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "A"
	}
	return name
}

func stableID(parts ...string) string {
	sum := stableidentity.Sum256(parts...)
	bytes := sum[:16]
	bytes[6] = bytes[6]&0x0f | 0x50
	bytes[8] = bytes[8]&0x3f | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
