package app

// Batch Grafana input preparation: parsing, stable-identity validation, and
// unique-target enforcement before any migration work begins.

import (
	"fmt"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/source/grafana"
	"github.com/mansiverma897993/noz-in/internal/stableidentity"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
)

type preparedGrafanaInput struct {
	path      string
	base      string
	dashboard model.Dashboard
}

type dashboardTargetIdentity struct {
	namespace string
	kind      string
	value     string
}

func prepareGrafanaInputs(paths, bases []string, options GrafanaOptions) ([]preparedGrafanaInput, error) {
	inputs := make([]preparedGrafanaInput, 0, len(paths))
	inputErrors := make([]error, 0)
	for pathIndex, path := range paths {
		dashboard, err := grafana.ParseFile(path)
		if err != nil {
			inputErrors = append(inputErrors, inputError(err))
			continue
		}
		if options.DashboardIdentity != "" {
			dashboard.Source.Identity = options.DashboardIdentity
		}
		if sourcePath := strings.TrimSpace(options.SourcePathOverrides[path]); sourcePath != "" {
			dashboard.Source.Path = sourcePath
		}
		dashboard.Source.Namespace = options.SourceNamespace
		if err := validateDashboardStableIdentity(dashboard); err != nil {
			inputErrors = append(inputErrors, inputError(fmt.Errorf("validate stable identity for %q: %w", path, err)))
			continue
		}
		applyVariableOverrides(&dashboard, options.Variables)
		inputs = append(inputs, preparedGrafanaInput{path: path, base: bases[pathIndex], dashboard: dashboard})
	}
	if len(inputErrors) != 0 {
		combined := combineGrafanaRunErrors(inputErrors)
		// In continue-on-error mode the parseable dashboards are still returned so
		// the batch migrates them; the caller combines the input errors into the
		// final outcome. Otherwise the batch is all-or-nothing.
		if options.ContinueOnInputError && len(inputs) > 0 {
			return inputs, combined
		}
		return nil, combined
	}
	if err := validateUniqueDashboardTargets(inputs); err != nil {
		return nil, inputError(err)
	}
	return inputs, nil
}

func validateUniqueDashboardTargets(inputs []preparedGrafanaInput) error {
	owners := make(map[string]preparedGrafanaInput, len(inputs))
	for _, input := range inputs {
		identity := targetIdentityForDashboard(input.dashboard)
		targetID := signoz.DashboardUUID(input.dashboard)
		if previous, exists := owners[targetID]; exists {
			return fmt.Errorf(
				"grafana dashboards %q (%q) and %q (%q) resolve to the same stable target identity %q (%s %q in namespace %q)",
				previous.path, previous.dashboard.Title, input.path, input.dashboard.Title,
				targetID, identity.kind, identity.value, identity.namespace,
			)
		}
		owners[targetID] = input
	}
	return nil
}

func targetIdentityForDashboard(dashboard model.Dashboard) dashboardTargetIdentity {
	identity := strings.TrimSpace(dashboard.Source.Identity)
	if identity == "" {
		identity = strings.TrimSpace(dashboard.Source.Path)
	}
	namespace := strings.TrimSpace(dashboard.Source.Namespace)
	if namespace == "" {
		namespace = identity
	}
	if uid := strings.TrimSpace(dashboard.UID); uid != "" {
		return dashboardTargetIdentity{namespace: namespace, kind: "UID", value: uid}
	}
	return dashboardTargetIdentity{namespace: namespace, kind: "source identity", value: identity}
}

func validateDashboardStableIdentity(dashboard model.Dashboard) error {
	components := []struct {
		field string
		value string
		limit int
	}{
		{field: "dashboard source namespace", value: dashboard.Source.Namespace, limit: 512},
		{field: "dashboard source identity", value: dashboard.Source.Identity, limit: 4096},
		{field: "dashboard source path", value: dashboard.Source.Path, limit: 4096},
		{field: "Grafana dashboard UID", value: dashboard.UID, limit: 1024},
	}
	for _, component := range components {
		if err := stableidentity.ValidateComponent(component.field, component.value, component.limit); err != nil {
			return err
		}
	}
	return nil
}

func applyVariableOverrides(dashboard *model.Dashboard, overrides map[string]string) {
	for index := range dashboard.Variables {
		value, ok := overrides[dashboard.Variables[index].Name]
		if !ok {
			continue
		}
		dashboard.Variables[index].Current = []string{value}
	}
}
