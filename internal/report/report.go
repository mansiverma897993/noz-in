package report

import (
	"fmt"
	"slices"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/version"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// Build creates an evidence report from a migration.
func Build(migration model.Migration) reporttypes.Report {
	inventory := reportInventory(migration.Dashboard)
	report := reporttypes.Report{
		SchemaVersion: "1",
		Tool:          reporttypes.Tool{Name: "promcast", Version: version.Version(), Commit: version.Commit()},
		Panels:        make([]reporttypes.PanelRecord, 0, len(migration.Dashboard.Panels)),
		Variables:     make([]reporttypes.VariableRecord, 0, len(migration.Dashboard.Variables)),
		Source: reporttypes.Source{
			Kind:          migration.Dashboard.Source.Kind,
			SchemaVersion: migration.Dashboard.Source.SchemaVersion,
			Path:          migration.Dashboard.Source.Path,
			Namespace:     migration.Dashboard.Source.Namespace,
			Identity:      migration.Dashboard.Source.Identity,
			SHA256:        migration.Dashboard.Source.SHA256,
		},
		SourceInventory: reporttypes.SourceInventory{
			Panels: inventory.Panels, Queries: inventory.Queries, Variables: inventory.Variables,
			SourceFeatures: inventory.SourceFeatures,
		},
		Dashboard: reporttypes.DashboardInfo{
			Title: migration.Dashboard.Title, GrafanaUID: migration.Dashboard.UID,
			SchemaVersion: migration.Dashboard.Source.SchemaVersion, Source: migration.Dashboard.Source.Path,
		},
		ReasonCodes: reasonCodeIndex(),
	}
	report.Summary.Panels = inventory.Panels
	report.Summary.Queries = inventory.Queries
	report.Summary.Variables = inventory.Variables
	report.Summary.SourceFeatures = inventory.SourceFeatures
	for _, feature := range migration.Dashboard.SourceFeatures {
		report.SourceFeatures = append(report.SourceFeatures, sourceFeatureRecord(feature))
		report.Summary.SourceFeaturesAccounted++
		report.Summary.SourceFeaturesNeedsReview++
	}
	for _, panel := range migration.Dashboard.Panels {
		mode := migration.PanelMode(panel)
		fallbackReason := migration.PanelFallbackReason(panel)
		panelDecision := migration.PanelDecision(panel)
		panelRecord := reporttypes.PanelRecord{
			ID:          panel.ID,
			Title:       panel.Title,
			Kind:        string(panel.Kind),
			SourceType:  panel.SourceType,
			EmittedKind: emittedPanelKind(panel, mode),
			SourcePath:  panel.SourcePath,
			EmittedMode: string(mode),
			Verdict:     string(panelDecision.Verdict),
			ReasonCodes: stringReasons(panelDecision.Reasons),
			Content:     panel.Content,
			Transforms:  append([]string(nil), panel.Transforms...),
			Repeat:      panel.Repeat,
			TimeFrom:    panel.TimeFrom,
			TimeShift:   panel.TimeShift,
			Queries:     make([]reporttypes.QueryRecord, 0, len(panel.Queries)),
		}
		for _, feature := range panel.SourceFeatures {
			panelRecord.SourceFeatures = append(panelRecord.SourceFeatures, sourceFeatureRecord(feature))
			report.Summary.SourceFeaturesAccounted++
			report.Summary.SourceFeaturesNeedsReview++
		}
		report.Summary.PanelsAccounted++
		switch panelDecision.Verdict {
		case model.VerdictNative:
			report.Summary.PanelsNative++
		case model.VerdictPassthrough:
			report.Summary.PanelsPassthrough++
		case model.VerdictNeedsReview:
			report.Summary.PanelsNeedsReview++
		}
		switch mode {
		case model.TranslationBuilder:
			report.Summary.BuilderPanels++
		case model.TranslationPromQL:
			report.Summary.PromQLPanels++
		default:
			report.Summary.PanelsOmitted++
		}

		for _, query := range panel.Queries {
			for range query.SourceFeatures {
				report.Summary.SourceFeaturesAccounted++
				report.Summary.SourceFeaturesNeedsReview++
			}
			translation, ok := migration.TranslationFor(query)
			if !ok {
				translation = model.Translation{
					Kind:     model.TranslationNone,
					Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonParseError}},
				}
			}
			record := queryRecord(query, translation, mode, fallbackReason)
			panelRecord.Queries = append(panelRecord.Queries, record)
			report.Summary.QueriesAccounted++
			switch record.Verdict {
			case string(model.VerdictNative):
				report.Summary.Native++
			case string(model.VerdictPassthrough):
				report.Summary.Passthrough++
			case string(model.VerdictNeedsReview):
				report.Summary.NeedsReview++
			}
			switch record.EmittedKind {
			case string(model.TranslationBuilder):
				report.Summary.Builder++
			case string(model.TranslationFormula):
				report.Summary.Formula++
			}
		}
		report.Panels = append(report.Panels, panelRecord)
	}
	for _, variable := range migration.Dashboard.Variables {
		translation, ok := migration.VariableTranslationFor(variable)
		if !ok {
			translation = model.VariableTranslation{Kind: "none", Decision: model.Decision{
				Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonUnsupportedVariable},
			}}
		}
		report.Summary.VariablesAccounted++
		if translation.Decision.Verdict == model.VerdictNeedsReview {
			report.Summary.VariablesNeedsReview++
		}
		for range variable.SourceFeatures {
			report.Summary.SourceFeaturesAccounted++
			report.Summary.SourceFeaturesNeedsReview++
		}
		report.Variables = append(report.Variables, reporttypes.VariableRecord{
			Name: variable.Name, Label: variable.Label, SourcePath: variable.SourcePath, SourceKind: string(variable.Kind),
			EmittedKind: translation.Kind, Attribute: translation.Attribute,
			Current: append([]string(nil), variable.Current...), AllValue: variable.AllValue,
			Verdict: string(translation.Decision.Verdict), ReasonCodes: stringReasons(translation.Decision.Reasons),
			Notes:          append([]string(nil), translation.Decision.Notes...),
			SourceFeatures: sourceFeatureRecords(variable.SourceFeatures),
		})
	}
	report.Summary.ReconciliationComplete =
		report.Summary.PanelsAccounted == report.Summary.Panels &&
			report.Summary.QueriesAccounted == report.Summary.Queries &&
			report.Summary.VariablesAccounted == report.Summary.Variables &&
			report.Summary.SourceFeaturesAccounted == report.Summary.SourceFeatures
	RefreshSummary(&report)
	return report
}

func reportInventory(dashboard model.Dashboard) model.SourceInventory {
	if dashboard.SourceInventory.Captured {
		return dashboard.SourceInventory
	}
	inventory := model.SourceInventory{
		Captured: true, Panels: len(dashboard.Panels), Variables: len(dashboard.Variables),
		SourceFeatures: len(dashboard.SourceFeatures),
	}
	for _, panel := range dashboard.Panels {
		inventory.Queries += len(panel.Queries)
		inventory.SourceFeatures += len(panel.SourceFeatures)
		for _, query := range panel.Queries {
			inventory.SourceFeatures += len(query.SourceFeatures)
		}
	}
	for _, variable := range dashboard.Variables {
		inventory.SourceFeatures += len(variable.SourceFeatures)
	}
	return inventory
}

func emittedPanelKind(panel model.Panel, mode model.TranslationKind) string {
	if mode == model.TranslationNone {
		return "omitted"
	}
	if panel.Kind == model.PanelKindBar || panel.Kind == model.PanelKindHistogram {
		return "graph"
	}
	if mode == model.TranslationPromQL &&
		(panel.Kind == model.PanelKindValue || panel.Kind == model.PanelKindTable || panel.Kind == model.PanelKindPie) {
		return "graph"
	}
	if panel.Kind == model.PanelKindText || panel.Kind == model.PanelKindUnknown {
		return "EMPTY_WIDGET"
	}
	return string(panel.Kind)
}

func queryRecord(
	query model.Query,
	translation model.Translation,
	panelMode model.TranslationKind,
	fallbackReason model.ReasonCode,
) reporttypes.QueryRecord {
	verdict := translation.Decision.Verdict
	reasons := append([]model.ReasonCode(nil), translation.Decision.Reasons...)
	for _, feature := range query.SourceFeatures {
		verdict = model.VerdictNeedsReview
		if !slices.Contains(reasons, feature.Reason) {
			reasons = append(reasons, feature.Reason)
		}
	}
	emittedKind := translation.Kind
	promQL := translation.PromQL
	if panelMode == model.TranslationNone {
		emittedKind = model.TranslationNone
		verdict = model.VerdictNeedsReview
		promQL = ""
		if !slices.Contains(reasons, model.ReasonPanelOmitted) {
			reasons = append(reasons, model.ReasonPanelOmitted)
		}
	} else if panelMode == model.TranslationPromQL && (translation.Kind == model.TranslationBuilder || translation.Kind == model.TranslationFormula) {
		emittedKind = model.TranslationPromQL
		if verdict != model.VerdictNeedsReview {
			verdict = model.VerdictPassthrough
		}
		if promQL == "" {
			promQL = query.Expression
		}
		if fallbackReason == "" {
			fallbackReason = model.ReasonMixedPanelQueries
		}
		if !slices.Contains(reasons, fallbackReason) {
			reasons = append(reasons, fallbackReason)
		}
	}
	return reporttypes.QueryRecord{
		RefID: query.RefID,
		OriginalRefID: func() string {
			if query.RefIDNormalized {
				return query.OriginalRefID
			}
			return ""
		}(),
		SourcePath:     query.SourcePath,
		Original:       query.Expression,
		OriginalLegend: query.Legend,
		EmittedLegend:  reportLegend(translation, query.Legend),
		Disabled:       query.Hidden,
		Instant:        query.Instant,
		Format:         query.Format,
		Step:           query.Step,
		Interval:       query.Interval,
		IntervalFactor: query.IntervalFactor,
		MaxDataPoints:  query.MaxDataPoints,
		CandidateKind:  string(translation.Kind),
		EmittedKind:    string(emittedKind),
		Verdict:        string(verdict),
		ReasonCodes:    stringReasons(reasons),
		Builder:        reportBuilder(translation.Builder),
		Formula:        reportFormula(translation.Formula),
		PromQL:         promQL,
		ParseErrors:    reportParseErrors(translation.ParseErrors),
		Notes:          append([]string(nil), translation.Decision.Notes...),
		SourceFeatures: sourceFeatureRecords(query.SourceFeatures),
	}
}

func reportLegend(translation model.Translation, fallback string) string {
	if translation.Legend != nil {
		return *translation.Legend
	}
	return fallback
}

func sourceFeatureRecord(feature model.SourceFeature) reporttypes.SourceFeatureRecord {
	return reporttypes.SourceFeatureRecord{
		Kind: feature.Kind, SourcePath: feature.SourcePath, Detail: feature.Detail,
		Verdict: string(model.VerdictNeedsReview), ReasonCode: string(feature.Reason),
	}
}

func sourceFeatureRecords(features []model.SourceFeature) []reporttypes.SourceFeatureRecord {
	if len(features) == 0 {
		return nil
	}
	records := make([]reporttypes.SourceFeatureRecord, 0, len(features))
	for _, feature := range features {
		records = append(records, sourceFeatureRecord(feature))
	}
	return records
}

func reportFormula(formula *model.Formula) *reporttypes.Formula {
	if formula == nil {
		return nil
	}
	result := &reporttypes.Formula{Name: formula.Name, Expression: formula.Expression}
	for index := range formula.Queries {
		builder := reportBuilder(&formula.Queries[index])
		result.Queries = append(result.Queries, *builder)
	}
	return result
}

func stringReasons(reasons []model.ReasonCode) []string {
	result := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		result = append(result, string(reason))
	}
	return result
}

func reportBuilder(builder *model.BuilderQuery) *reporttypes.BuilderQuery {
	if builder == nil {
		return nil
	}
	result := &reporttypes.BuilderQuery{
		Name:             builder.Name,
		MetricName:       builder.MetricName,
		Temporality:      builder.Temporality,
		TimeAggregation:  builder.TimeAggregation,
		SpaceAggregation: builder.SpaceAggregation,
		GroupBy:          append([]string(nil), builder.GroupBy...),
		StepSeconds:      builder.StepSeconds,
	}
	for _, filter := range builder.Filters {
		result.Filters = append(result.Filters, reporttypes.Filter{Label: filter.Label, Operator: filter.Operator, Value: filter.Value})
	}
	for _, function := range builder.Functions {
		result.Functions = append(result.Functions, reporttypes.Function{Name: function.Name, Args: append([]float64(nil), function.Args...)})
	}
	return result
}

// RefreshSummary recomputes derived report claims after live validation changes records.
func RefreshSummary(report *reporttypes.Report) {
	report.Summary.DataPresentPercent = 0
	if report.Summary.ValidationEligible > 0 {
		report.Summary.DataPresentPercent = float64(report.Summary.DataPresent) / float64(report.Summary.ValidationEligible) * 100
	}
	report.Summary.Headline = fmt.Sprintf(
		"%d native (%d builder, %d formula), %d passthrough, %d queries need review; %d/%d panels accounted for, %d omitted; review scope: %d panels, %d variables, %d source features",
		report.Summary.Native,
		report.Summary.Builder,
		report.Summary.Formula,
		report.Summary.Passthrough,
		report.Summary.NeedsReview,
		report.Summary.PanelsAccounted,
		report.Summary.Panels,
		report.Summary.PanelsOmitted,
		report.Summary.PanelsNeedsReview,
		report.Summary.VariablesNeedsReview,
		report.Summary.SourceFeaturesNeedsReview,
	)
	for index := range report.Panels {
		report.Panels[index].State = panelState(report.Panels[index])
	}
}

func panelState(panel reporttypes.PanelRecord) string {
	if panel.Verdict == string(model.VerdictNeedsReview) {
		return "needs-review"
	}
	eligibleQueries := 0
	allQueriesReturnedData := true
	for _, query := range panel.Queries {
		if query.Disabled || query.EmittedKind == string(model.TranslationNone) {
			continue
		}
		eligibleQueries++
		allQueriesReturnedData = allQueriesReturnedData && query.Validation.Executed && query.Validation.DataPresent
	}
	allQueriesReturnedData = eligibleQueries > 0 && allQueriesReturnedData
	if panel.Verdict == string(model.VerdictPassthrough) {
		if allQueriesReturnedData {
			return "passthrough-and-data-present"
		}
		return "passthrough-without-data-evidence"
	}
	if allQueriesReturnedData {
		return "transpiled-and-data-present"
	}
	return "transpiled-without-data-evidence"
}

func reportParseErrors(parseErrors []model.ParseError) []reporttypes.ParseError {
	result := make([]reporttypes.ParseError, 0, len(parseErrors))
	for _, parseError := range parseErrors {
		result = append(result, reporttypes.ParseError{Message: parseError.Message, Start: parseError.Start, End: parseError.End})
	}
	return result
}

func reasonCodeIndex() map[string]string {
	index := make(map[string]string)
	for _, code := range model.ReasonCodes() {
		description, ok := model.ReasonDescription(code)
		if ok {
			index[string(code)] = description
		}
	}
	return index
}
