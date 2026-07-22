package app

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// ValidateStoredDashboardArtifact verifies the exact bytes, portable filename,
// strict v5 shape, and query mapping of a primary dashboard artifact. Callers
// must use this before live work; structural evidence alone does not bind
// titles, variables, layouts, or other non-query bytes.
func ValidateStoredDashboardArtifact(
	path string,
	data []byte,
	evidence reporttypes.Report,
) (signoz.DashboardV5, error) {
	if evidence.PrimaryArtifact == nil {
		return signoz.DashboardV5{}, fmt.Errorf("migration report has no primary dashboard artifact binding; rerun migration")
	}
	binding := *evidence.PrimaryArtifact
	if err := validatePrimaryArtifactBinding(binding); err != nil {
		return signoz.DashboardV5{}, fmt.Errorf("invalid primary dashboard artifact binding: %w", err)
	}
	if filepath.Base(path) != binding.Path {
		return signoz.DashboardV5{}, fmt.Errorf(
			"primary dashboard filename %q does not match migration evidence filename %q",
			filepath.Base(path), binding.Path,
		)
	}
	if int64(len(data)) != binding.SizeBytes {
		return signoz.DashboardV5{}, fmt.Errorf(
			"primary dashboard size %d does not match migration evidence size %d",
			len(data), binding.SizeBytes,
		)
	}
	digest := sha256.Sum256(data)
	actualHash := fmt.Sprintf("%x", digest[:])
	if actualHash != binding.SHA256 {
		return signoz.DashboardV5{}, fmt.Errorf(
			"primary dashboard SHA-256 %q does not match migration evidence SHA-256 %q",
			actualHash, binding.SHA256,
		)
	}
	var dashboard signoz.DashboardV5
	if err := decodeStrictJSON(data, &dashboard); err != nil {
		return signoz.DashboardV5{}, fmt.Errorf("decode primary dashboard: %w", err)
	}
	if err := ValidateStoredDashboardEvidence(&dashboard, evidence); err != nil {
		return signoz.DashboardV5{}, fmt.Errorf("verify primary dashboard evidence: %w", err)
	}
	return dashboard, nil
}

// ValidateStoredDashboardEvidence verifies that a persisted primary dashboard
// is exactly the artifact described by migration evidence. It restores the
// non-serialized widget SourcePath only after mapping each widget ID back to a
// unique primary panel. Reports created before emitted-spec bindings existed
// are rejected and must be regenerated.
func ValidateStoredDashboardEvidence(dashboard *signoz.DashboardV5, evidence reporttypes.Report) error {
	if dashboard == nil {
		return fmt.Errorf("stored dashboard is nil")
	}
	panelsByWidgetID, primaryWidgetIDs, err := indexPrimaryPanelEvidence(evidence.Panels)
	if err != nil {
		return err
	}
	widgetsByID, err := bindStoredWidgets(dashboard, panelsByWidgetID, primaryWidgetIDs)
	if err != nil {
		return err
	}
	return validatePrimaryPanelQueries(*dashboard, evidence, widgetsByID)
}

func indexPrimaryPanelEvidence(
	panels []reporttypes.PanelRecord,
) (map[string]reporttypes.PanelRecord, map[string]bool, error) {
	panelsByWidgetID := make(map[string]reporttypes.PanelRecord, len(panels))
	primaryWidgetIDs := make(map[string]bool)
	panelPaths := make(map[string]bool, len(panels))
	for _, panel := range panels {
		if strings.TrimSpace(panel.SourcePath) == "" {
			return nil, nil, fmt.Errorf("migration report contains a panel with an empty source path")
		}
		if panelPaths[panel.SourcePath] {
			return nil, nil, fmt.Errorf("migration report contains duplicate panel source path %q", panel.SourcePath)
		}
		panelPaths[panel.SourcePath] = true
		if panel.EmittedWidgetID != "" {
			if previous, duplicate := panelsByWidgetID[panel.EmittedWidgetID]; duplicate {
				return nil, nil, fmt.Errorf(
					"emitted widget id %q maps to both %q and %q",
					panel.EmittedWidgetID, previous.SourcePath, panel.SourcePath,
				)
			}
			panelsByWidgetID[panel.EmittedWidgetID] = panel
		}
		if panel.PrimaryArtifact {
			if strings.TrimSpace(panel.EmittedWidgetID) == "" {
				return nil, nil, fmt.Errorf("primary panel %q has no emitted widget id", panel.SourcePath)
			}
			primaryWidgetIDs[panel.EmittedWidgetID] = true
		}
	}
	return panelsByWidgetID, primaryWidgetIDs, nil
}

func bindStoredWidgets(
	dashboard *signoz.DashboardV5,
	panelsByWidgetID map[string]reporttypes.PanelRecord,
	primaryWidgetIDs map[string]bool,
) (map[string]signoz.Widget, error) {
	widgetsByID := make(map[string]signoz.Widget, len(dashboard.Widgets))
	for index := range dashboard.Widgets {
		widget := &dashboard.Widgets[index]
		if strings.TrimSpace(widget.ID) == "" {
			return nil, fmt.Errorf("stored widget %q has an empty id", widget.Title)
		}
		if _, duplicate := widgetsByID[widget.ID]; duplicate {
			return nil, fmt.Errorf("stored dashboard contains duplicate widget id %q", widget.ID)
		}
		panel, found := panelsByWidgetID[widget.ID]
		if !found {
			return nil, fmt.Errorf("stored widget %q (%q) has no mapping in the migration report", widget.ID, widget.Title)
		}
		if !panel.PrimaryArtifact {
			return nil, fmt.Errorf("stored widget %q maps to non-primary panel %q", widget.ID, panel.SourcePath)
		}
		widget.SourcePath = panel.SourcePath
		widgetsByID[widget.ID] = *widget
	}
	for widgetID := range primaryWidgetIDs {
		if _, found := widgetsByID[widgetID]; !found {
			return nil, fmt.Errorf("primary widget %q is missing from the stored dashboard", widgetID)
		}
	}
	return widgetsByID, nil
}

func validatePrimaryPanelQueries(
	dashboard signoz.DashboardV5,
	evidence reporttypes.Report,
	widgetsByID map[string]signoz.Widget,
) error {
	queryPaths := make(map[string]bool)
	for _, panel := range evidence.Panels {
		if !panel.PrimaryArtifact {
			continue
		}
		widget := widgetsByID[panel.EmittedWidgetID]
		hasEmittedQuery, err := validateStoredPanelQueries(dashboard, evidence, panel, widget, queryPaths)
		if err != nil {
			return err
		}
		if !hasEmittedQuery {
			request, err := signoz.PreviewRequestForWidgetWindow(widget, nil, time.Unix(3600, 0), time.Hour)
			if err != nil {
				return fmt.Errorf("inspect stored widget %q: %w", widget.ID, err)
			}
			if len(request.CompositeQuery.Queries) != 0 {
				return fmt.Errorf("stored widget %q contains queries absent from the migration report", widget.ID)
			}
		}
	}
	return nil
}

func validateStoredPanelQueries(
	dashboard signoz.DashboardV5,
	evidence reporttypes.Report,
	panel reporttypes.PanelRecord,
	widget signoz.Widget,
	queryPaths map[string]bool,
) (bool, error) {
	hasEmittedQuery := false
	for _, query := range panel.Queries {
		if strings.TrimSpace(query.SourcePath) == "" {
			return false, fmt.Errorf("primary panel %q contains a query with an empty source path", panel.SourcePath)
		}
		if queryPaths[query.SourcePath] {
			return false, fmt.Errorf("migration report contains duplicate primary query source path %q", query.SourcePath)
		}
		queryPaths[query.SourcePath] = true
		recorded, err := recordedQueryIdentity(query)
		if err != nil {
			return false, err
		}
		current, emitted, err := storedQueryIdentity(widget, query, recorded.TargetKind)
		if err != nil {
			return false, err
		}
		hasEmittedQuery = hasEmittedQuery || emitted
		if current != recorded {
			return false, fmt.Errorf("primary query %q emitted specification does not match the migration report", query.SourcePath)
		}
		if query.EmittedKind == "promql" {
			if err := validateStoredPromQLVariableInterpolation(dashboard, evidence, query); err != nil {
				return false, fmt.Errorf("primary query %q variable interpolation: %w", query.SourcePath, err)
			}
		}
	}
	return hasEmittedQuery, nil
}

func storedQueryIdentity(
	widget signoz.Widget,
	query reporttypes.QueryRecord,
	targetKind string,
) (emittedQueryIdentity, bool, error) {
	if targetKind == targetKindNone {
		identity, err := nonEmittedQuerySpec(query.RefID)
		return identity, false, err
	}
	identity, found, err := emittedQuerySpec(widget, query.RefID)
	if err == nil && !found {
		return emittedQueryIdentity{}, false, fmt.Errorf(
			"primary query %q is missing from stored widget %q", query.SourcePath, widget.ID,
		)
	}
	if err != nil {
		return emittedQueryIdentity{}, false, fmt.Errorf("validate primary query %q: %w", query.SourcePath, err)
	}
	return identity, true, nil
}

func validateStoredPromQLVariableInterpolation(
	dashboard signoz.DashboardV5,
	evidence reporttypes.Report,
	query reporttypes.QueryRecord,
) error {
	variableNames := make([]string, 0, len(evidence.Variables))
	for _, variable := range evidence.Variables {
		variableNames = append(variableNames, variable.Name)
	}
	if !transpile.TargetPromQLRuntimeSubstitutionExact(
		query.Original, query.EmittedExpression, variableNames,
	) {
		return fmt.Errorf("pinned SigNoz runtime-variable or Go-template rendering diverges from Grafana; rerun migration")
	}
	sourceNames := transpile.VariableNames(query.Original)
	for _, name := range transpile.VariableNames(query.EmittedExpression) {
		if !slices.Contains(sourceNames, name) {
			return fmt.Errorf("emitted variable %q has no source interpolation binding; rerun migration", name)
		}
		variableEvidence, err := exactStoredVariableEvidence(evidence, name)
		if err != nil {
			return err
		}
		storedVariable, err := exactStoredDashboardVariable(dashboard, name)
		if err != nil {
			return err
		}
		if len(variableEvidence.Current) == 0 || slices.ContainsFunc(variableEvidence.Current, func(value string) bool {
			return strings.TrimSpace(value) == ""
		}) {
			return fmt.Errorf("variable %q has no proven nonblank current selection; rerun migration", name)
		}
		if storedVariable.Type == "DYNAMIC" && storedVariable.AllSelected &&
			storedVariable.ShowAllOption && storedVariable.MultiSelect {
			if strings.TrimSpace(variableEvidence.AllValue) == ".*" &&
				transpile.TargetDynamicAllMatcherRemovalExact(query.Original, name) {
				continue
			}
			return fmt.Errorf(
				"dynamic All variable %q is not an explicit .* confined to complete positive regex matchers; rerun migration",
				name,
			)
		}
		if storedVariable.Type == "DYNAMIC" && len(variableEvidence.Current) == 1 &&
			variableEvidence.Current[0] == "__all__" {
			return fmt.Errorf(
				"dynamic variable %q has a literal __all__ value that pinned SigNoz treats as matcher removal; rerun migration",
				name,
			)
		}
		if !transpile.TargetRawVariableSubstitutionExact(
			query.Original,
			name,
			variableEvidence.Current,
			storedVariable.MultiSelect || storedVariable.ShowAllOption,
			variableNames,
		) {
			return fmt.Errorf(
				"grafana interpolation of variable %q diverges from pinned SigNoz raw selectedValue substitution; rerun migration",
				name,
			)
		}
	}
	return nil
}

func exactStoredVariableEvidence(evidence reporttypes.Report, name string) (reporttypes.VariableRecord, error) {
	var matches []reporttypes.VariableRecord
	for _, variable := range evidence.Variables {
		if variable.Name == name {
			matches = append(matches, variable)
		}
	}
	if len(matches) != 1 {
		return reporttypes.VariableRecord{}, fmt.Errorf(
			"expected exactly one migration variable named %q, found %d; rerun migration",
			name,
			len(matches),
		)
	}
	return matches[0], nil
}

func exactStoredDashboardVariable(dashboard signoz.DashboardV5, name string) (signoz.VariableV5, error) {
	var matches []signoz.VariableV5
	for _, variable := range dashboard.Variables {
		if variable.Name == name {
			matches = append(matches, variable)
		}
	}
	if len(matches) != 1 {
		return signoz.VariableV5{}, fmt.Errorf(
			"expected exactly one stored dashboard variable named %q, found %d; rerun migration",
			name,
			len(matches),
		)
	}
	return matches[0], nil
}

func recordedQueryIdentity(query reporttypes.QueryRecord) (emittedQueryIdentity, error) {
	targetKind, ok := targetKindForEmittedKind(query.EmittedKind)
	if !ok {
		return emittedQueryIdentity{}, fmt.Errorf("migration query %q has missing or unsupported emitted kind %q", query.SourcePath, query.EmittedKind)
	}
	expectedName, err := migrationTargetQueryName(query)
	if err != nil {
		return emittedQueryIdentity{}, err
	}
	if query.EmittedQueryName != expectedName {
		return emittedQueryIdentity{}, fmt.Errorf("migration query %q has an inconsistent emitted query name", query.SourcePath)
	}
	if !validSHA256(query.EmittedSpecSHA256) {
		return emittedQueryIdentity{}, fmt.Errorf("migration query %q has no valid emitted specification SHA-256; rerun migration", query.SourcePath)
	}
	switch query.EmittedKind {
	case "promql":
		if query.EmittedExpression != query.PromQL {
			return emittedQueryIdentity{}, fmt.Errorf("migration query %q has an inconsistent emitted PromQL expression", query.SourcePath)
		}
	case "formula":
		if query.Formula == nil || query.EmittedExpression != query.Formula.Expression {
			return emittedQueryIdentity{}, fmt.Errorf("migration query %q has an inconsistent emitted formula expression", query.SourcePath)
		}
	case "builder":
		var spec signoz.BuilderQuerySpec
		if err := decodeStrictJSON([]byte(query.EmittedExpression), &spec); err != nil || spec.Name != query.EmittedQueryName {
			return emittedQueryIdentity{}, fmt.Errorf("migration query %q has an invalid emitted Builder expression", query.SourcePath)
		}
	case "none":
		if query.EmittedExpression != "" {
			return emittedQueryIdentity{}, fmt.Errorf("non-emitted migration query %q has an emitted expression", query.SourcePath)
		}
	}
	return emittedQueryIdentity{
		TargetKind:       targetKind,
		TargetQueryName:  query.EmittedQueryName,
		TargetExpression: query.EmittedExpression,
		SHA256:           query.EmittedSpecSHA256,
	}, nil
}
