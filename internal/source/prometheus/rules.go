package prometheus

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
	prommodel "github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/prometheus/prometheus/promql/parser"
	"gopkg.in/yaml.v3"
)

const (
	maxRuleFileSize          = 16 << 20
	maxMaterializedYAMLNodes = 2_000_000
	maxYAMLAliasDepth        = 512
)

var (
	plainRuleObjectKeys = map[string]struct{}{
		"groups": {},
	}
	prometheusRuleKeys = map[string]struct{}{
		"apiVersion": {}, "kind": {}, "metadata": {}, "spec": {}, "status": {},
	}
	prometheusRuleSpecKeys = map[string]struct{}{
		"groups": {},
	}
	kubernetesListKeys = map[string]struct{}{
		"apiVersion": {}, "kind": {}, "metadata": {}, "items": {},
	}
)

// ParseFile reads plain Prometheus rule files and PrometheusRule resources.
func ParseFile(path string) (model.RuleSet, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return model.RuleSet{}, fmt.Errorf("open Prometheus rules %q: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxRuleFileSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return model.RuleSet{}, fmt.Errorf("read Prometheus rules %q: %w", path, readErr)
	}
	if closeErr != nil {
		return model.RuleSet{}, fmt.Errorf("close Prometheus rules %q: %w", path, closeErr)
	}
	if len(data) > maxRuleFileSize {
		return model.RuleSet{}, fmt.Errorf("prometheus rules %q exceed %d bytes", path, maxRuleFileSize)
	}
	return Parse(data, path)
}

// Parse unwraps every supported YAML object, validates each groups collection
// with Prometheus v0.311.3 rulefmt, and then normalizes it. Group-name
// uniqueness therefore follows Prometheus's object/file scope: one plain YAML
// document, one PrometheusRule resource, or one item in a Kubernetes List.
func Parse(data []byte, path string) (model.RuleSet, error) {
	digest := sha256.Sum256(data)
	result := model.RuleSet{Source: model.Source{
		Kind:     "prometheus_rules",
		Path:     path,
		Identity: path,
		SHA256:   fmt.Sprintf("%x", digest[:]),
	}}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	documents := 0
	for documentIndex := 0; ; documentIndex++ {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			if err == io.EOF {
				break
			}
			return model.RuleSet{}, fmt.Errorf("decode Prometheus rules %q document %d: %w", path, documentIndex, err)
		}
		documents++
		sourcePath := fmt.Sprintf("/documents/%d", documentIndex)
		if err := appendRuleObject(&result, &document, sourcePath, false); err != nil {
			return model.RuleSet{}, fmt.Errorf("validate Prometheus rules %q%s: %w", path, sourcePath, err)
		}
	}
	if documents == 0 {
		return model.RuleSet{}, fmt.Errorf("validate Prometheus rules %q: input is semantically empty", path)
	}
	return result, nil
}

func appendRuleObject(result *model.RuleSet, node *yaml.Node, sourcePath string, listItem bool) error {
	root := resolveNode(node)
	if root == nil || isNullNode(root) {
		return fmt.Errorf("object is semantically empty")
	}
	fields, err := mappingFields(root, "rule object")
	if err != nil {
		return err
	}

	if groups, ok := fields["groups"]; ok {
		if listItem {
			return fmt.Errorf("kubernetes List item must be a PrometheusRule resource, not a plain groups object")
		}
		if err := rejectUnsupportedFields(fields, plainRuleObjectKeys, "plain Prometheus rule object"); err != nil {
			return err
		}
		return appendValidatedGroups(result, groups, sourcePath+"/groups")
	}

	kindNode, ok := fields["kind"]
	if !ok {
		return fmt.Errorf("object has no supported semantic content; expected groups or Kubernetes kind")
	}
	kind, err := requiredScalar(kindNode, "Kubernetes kind")
	if err != nil {
		return err
	}
	switch kind {
	case "PrometheusRule":
		return appendPrometheusRule(result, fields, sourcePath)
	case "List", "PrometheusRuleList":
		return appendKubernetesList(result, fields, sourcePath)
	default:
		return fmt.Errorf("unsupported Kubernetes kind %q; expected PrometheusRule, List, or PrometheusRuleList", kind)
	}
}

func appendPrometheusRule(result *model.RuleSet, fields map[string]*yaml.Node, sourcePath string) error {
	if err := rejectUnsupportedFields(fields, prometheusRuleKeys, "PrometheusRule resource"); err != nil {
		return err
	}
	if err := validateKubernetesTypeMeta(fields); err != nil {
		return err
	}
	if err := validateOptionalMetadata(fields); err != nil {
		return err
	}
	if err := validateOptionalStatus(fields); err != nil {
		return err
	}
	specNode, ok := fields["spec"]
	if !ok {
		return fmt.Errorf("prometheusRule resource has no spec")
	}
	spec, err := mappingFields(resolveNode(specNode), "PrometheusRule spec")
	if err != nil {
		return err
	}
	if err := rejectUnsupportedFields(spec, prometheusRuleSpecKeys, "PrometheusRule spec"); err != nil {
		return err
	}
	groups, ok := spec["groups"]
	if !ok {
		return fmt.Errorf("prometheusRule spec has no groups")
	}
	return appendValidatedGroups(result, groups, sourcePath+"/spec/groups")
}

func appendKubernetesList(result *model.RuleSet, fields map[string]*yaml.Node, sourcePath string) error {
	if err := rejectUnsupportedFields(fields, kubernetesListKeys, "Kubernetes List resource"); err != nil {
		return err
	}
	if err := validateKubernetesTypeMeta(fields); err != nil {
		return err
	}
	if err := validateOptionalMetadata(fields); err != nil {
		return err
	}
	itemsNode, ok := fields["items"]
	if !ok {
		return fmt.Errorf("kubernetes List resource has no items")
	}
	items := resolveNode(itemsNode)
	if items == nil || items.Kind != yaml.SequenceNode {
		return fmt.Errorf("kubernetes List items must be a sequence")
	}
	if len(items.Content) == 0 {
		return fmt.Errorf("kubernetes List resource is semantically empty")
	}
	for index, item := range items.Content {
		itemPath := fmt.Sprintf("%s/items/%d", sourcePath, index)
		if err := appendRuleObject(result, item, itemPath, true); err != nil {
			return fmt.Errorf("item %d: %w", index, err)
		}
	}
	return nil
}

func appendValidatedGroups(result *model.RuleSet, groupsNode *yaml.Node, sourcePath string) error {
	groupsNode = resolveNode(groupsNode)
	if groupsNode == nil || groupsNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("groups must be a sequence")
	}
	if len(groupsNode.Content) == 0 {
		return fmt.Errorf("groups collection is semantically empty")
	}
	if err := validateDuplicateRuleMaps(groupsNode, sourcePath); err != nil {
		return err
	}

	content, err := marshalGroupsObject(groupsNode)
	if err != nil {
		return fmt.Errorf("marshal groups for Prometheus validation: %w", err)
	}
	parsed, validationErrors := rulefmt.Parse(
		content,
		false,
		prommodel.UTF8Validation,
		parser.NewParser(parser.Options{}),
	)
	if len(validationErrors) > 0 {
		return fmt.Errorf("prometheus v0.311.3 rulefmt rejected object: %w", errors.Join(validationErrors...))
	}
	if parsed == nil {
		return fmt.Errorf("prometheus v0.311.3 rulefmt returned no groups")
	}
	return appendNormalizedGroups(result, parsed.Groups, groupsNode, sourcePath)
}

func marshalGroupsObject(groupsNode *yaml.Node) ([]byte, error) {
	materialized, err := materializeYAMLAliases(groupsNode)
	if err != nil {
		return nil, err
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "groups"},
		materialized,
	}}
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	content, err := yaml.Marshal(document)
	if err != nil {
		return nil, err
	}
	if len(content) > maxRuleFileSize {
		return nil, fmt.Errorf("materialized rule object exceeds %d bytes", maxRuleFileSize)
	}
	return content, nil
}

type yamlAliasMaterializer struct {
	remaining int
	visiting  map[*yaml.Node]bool
}

func materializeYAMLAliases(node *yaml.Node) (*yaml.Node, error) {
	materializer := yamlAliasMaterializer{
		remaining: maxMaterializedYAMLNodes,
		visiting:  make(map[*yaml.Node]bool),
	}
	return materializer.clone(node, 0)
}

func (materializer *yamlAliasMaterializer) clone(node *yaml.Node, depth int) (*yaml.Node, error) {
	if node == nil {
		return nil, fmt.Errorf("YAML alias has no target")
	}
	if depth > maxYAMLAliasDepth {
		return nil, fmt.Errorf("YAML alias expansion exceeds depth %d", maxYAMLAliasDepth)
	}
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil {
			return nil, fmt.Errorf("YAML alias %q has no target", node.Value)
		}
		return materializer.clone(node.Alias, depth+1)
	}
	if materializer.visiting[node] {
		return nil, fmt.Errorf("recursive YAML alias references anchor %q", node.Anchor)
	}
	if materializer.remaining == 0 {
		return nil, fmt.Errorf("YAML alias expansion exceeds %d nodes", maxMaterializedYAMLNodes)
	}
	materializer.remaining--
	materializer.visiting[node] = true
	defer delete(materializer.visiting, node)

	cloned := *node
	cloned.Anchor = ""
	cloned.Alias = nil
	cloned.Content = make([]*yaml.Node, 0, len(node.Content))
	for _, child := range node.Content {
		materialized, err := materializer.clone(child, depth+1)
		if err != nil {
			return nil, err
		}
		cloned.Content = append(cloned.Content, materialized)
	}
	return &cloned, nil
}

func appendNormalizedGroups(
	result *model.RuleSet,
	parsed []rulefmt.RuleGroup,
	rawGroups *yaml.Node,
	sourcePath string,
) error {
	if len(parsed) != len(rawGroups.Content) {
		return fmt.Errorf("prometheus validation changed group cardinality from %d to %d", len(rawGroups.Content), len(parsed))
	}
	for groupIndex, source := range parsed {
		rawGroup := resolveNode(rawGroups.Content[groupIndex])
		groupPath := fmt.Sprintf("%s/%d", sourcePath, groupIndex)
		group := model.RuleGroup{
			Name:        source.Name,
			Interval:    rawDuration(rawGroup, "interval", source.Interval),
			QueryOffset: rawOptionalDuration(rawGroup, "query_offset", source.QueryOffset),
			Limit:       source.Limit,
			Labels:      copyMap(source.Labels),
			SourcePath:  groupPath,
		}

		rawRules := resolveNode(mappingValue(rawGroup, "rules"))
		if len(source.Rules) > 0 && (rawRules == nil || rawRules.Kind != yaml.SequenceNode) {
			return fmt.Errorf("validated group %q has no raw rules sequence", source.Name)
		}
		if rawRules != nil && len(source.Rules) != len(rawRules.Content) {
			return fmt.Errorf(
				"prometheus validation changed rule cardinality in group %q from %d to %d",
				source.Name, len(rawRules.Content), len(source.Rules),
			)
		}
		for ruleIndex, sourceRule := range source.Rules {
			rawRule := resolveNode(rawRules.Content[ruleIndex])
			group.Rules = append(group.Rules, model.Rule{
				Alert:         sourceRule.Alert,
				Record:        sourceRule.Record,
				Expression:    sourceRule.Expr,
				For:           rawDuration(rawRule, "for", sourceRule.For),
				KeepFiringFor: rawDuration(rawRule, "keep_firing_for", sourceRule.KeepFiringFor),
				Labels:        copyMap(sourceRule.Labels),
				Annotations:   copyMap(sourceRule.Annotations),
				SourcePath:    fmt.Sprintf("%s/rules/%d", groupPath, ruleIndex),
			})
		}
		result.Groups = append(result.Groups, group)
	}
	return nil
}

func validateDuplicateRuleMaps(groups *yaml.Node, sourcePath string) error {
	for groupIndex, groupNode := range groups.Content {
		group := resolveNode(groupNode)
		groupPath := fmt.Sprintf("%s/%d", sourcePath, groupIndex)
		if labels := mappingValue(group, "labels"); labels != nil {
			if err := validateStringMapKeys(labels, groupPath+"/labels", make(map[*yaml.Node]bool)); err != nil {
				return err
			}
		}
		rules := resolveNode(mappingValue(group, "rules"))
		if rules == nil || rules.Kind != yaml.SequenceNode {
			continue
		}
		for ruleIndex, ruleNode := range rules.Content {
			rule := resolveNode(ruleNode)
			rulePath := fmt.Sprintf("%s/rules/%d", groupPath, ruleIndex)
			for _, field := range []string{"labels", "annotations"} {
				if values := mappingValue(rule, field); values != nil {
					if err := validateStringMapKeys(values, rulePath+"/"+field, make(map[*yaml.Node]bool)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validateStringMapKeys(node *yaml.Node, sourcePath string, visiting map[*yaml.Node]bool) error {
	node = resolveNode(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil // rulefmt owns the authoritative type error.
	}
	if visiting[node] {
		return fmt.Errorf("%s contains a recursive YAML alias", sourcePath)
	}
	visiting[node] = true
	defer delete(visiting, node)

	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := resolveNode(node.Content[index])
		value := node.Content[index+1]
		if key == nil || key.Kind != yaml.ScalarNode {
			continue // rulefmt owns the authoritative key-type error.
		}
		if key.Value == "<<" {
			if err := validateMergeMaps(value, sourcePath, visiting); err != nil {
				return err
			}
			continue
		}
		if _, duplicate := seen[key.Value]; duplicate {
			return fmt.Errorf("%s contains duplicate YAML map key %q", sourcePath, key.Value)
		}
		seen[key.Value] = struct{}{}
	}
	return nil
}

func validateMergeMaps(node *yaml.Node, sourcePath string, visiting map[*yaml.Node]bool) error {
	node = resolveNode(node)
	if node == nil {
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if err := validateStringMapKeys(item, sourcePath, visiting); err != nil {
				return err
			}
		}
		return nil
	}
	return validateStringMapKeys(node, sourcePath, visiting)
}

func validateKubernetesTypeMeta(fields map[string]*yaml.Node) error {
	apiVersion, ok := fields["apiVersion"]
	if !ok {
		return fmt.Errorf("kubernetes resource has no apiVersion")
	}
	_, err := requiredScalar(apiVersion, "Kubernetes apiVersion")
	return err
}

func validateOptionalMetadata(fields map[string]*yaml.Node) error {
	metadata, ok := fields["metadata"]
	if !ok {
		return nil
	}
	metadata = resolveNode(metadata)
	if metadata == nil || metadata.Kind != yaml.MappingNode {
		return fmt.Errorf("kubernetes metadata must be a mapping")
	}
	return nil
}

func validateOptionalStatus(fields map[string]*yaml.Node) error {
	status, ok := fields["status"]
	if !ok {
		return nil
	}
	status = resolveNode(status)
	if status == nil || isNullNode(status) {
		return nil
	}
	if status.Kind != yaml.MappingNode {
		return fmt.Errorf("kubernetes status must be a mapping or null")
	}
	return nil
}

func mappingFields(node *yaml.Node, context string) (map[string]*yaml.Node, error) {
	node = resolveNode(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", context)
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := resolveNode(node.Content[index])
		if key == nil || key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s keys must be scalars", context)
		}
		if _, duplicate := fields[key.Value]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate field %q", context, key.Value)
		}
		fields[key.Value] = node.Content[index+1]
	}
	return fields, nil
}

func rejectUnsupportedFields(fields map[string]*yaml.Node, allowed map[string]struct{}, context string) error {
	unsupported := make([]string, 0)
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			unsupported = append(unsupported, field)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	sort.Strings(unsupported)
	return fmt.Errorf("%s contains unsupported fields %q", context, unsupported)
}

func requiredScalar(node *yaml.Node, context string) (string, error) {
	node = resolveNode(node)
	if node == nil || node.Kind != yaml.ScalarNode || isNullNode(node) {
		return "", fmt.Errorf("%s must be a nonempty scalar", context)
	}
	value := node.Value
	canonical := strings.TrimSpace(value)
	if canonical == "" {
		return "", fmt.Errorf("%s must be a nonempty scalar", context)
	}
	if canonical != value {
		return "", fmt.Errorf("%s must not contain surrounding whitespace", context)
	}
	return value, nil
}

func resolveNode(node *yaml.Node) *yaml.Node {
	for node != nil {
		switch node.Kind {
		case yaml.DocumentNode:
			if len(node.Content) == 0 {
				return nil
			}
			node = node.Content[0]
		case yaml.AliasNode:
			node = node.Alias
		default:
			return node
		}
	}
	return nil
}

func isNullNode(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

func mappingValue(node *yaml.Node, field string) *yaml.Node {
	return mappingValueSeen(resolveNode(node), field, make(map[*yaml.Node]bool))
}

func mappingValueSeen(node *yaml.Node, field string, visiting map[*yaml.Node]bool) *yaml.Node {
	node = resolveNode(node)
	if node == nil || node.Kind != yaml.MappingNode || visiting[node] {
		return nil
	}
	visiting[node] = true
	defer delete(visiting, node)

	var merges []*yaml.Node
	for index := 0; index < len(node.Content); index += 2 {
		key := resolveNode(node.Content[index])
		if key == nil || key.Kind != yaml.ScalarNode {
			continue
		}
		if key.Value == field {
			return node.Content[index+1]
		}
		if key.Value == "<<" {
			merges = append(merges, node.Content[index+1])
		}
	}
	for _, merge := range merges {
		merge = resolveNode(merge)
		if merge == nil {
			continue
		}
		if merge.Kind == yaml.SequenceNode {
			for _, candidate := range merge.Content {
				if value := mappingValueSeen(resolveNode(candidate), field, visiting); value != nil {
					return value
				}
			}
			continue
		}
		if value := mappingValueSeen(merge, field, visiting); value != nil {
			return value
		}
	}
	return nil
}

type durationStringer interface {
	String() string
}

func rawDuration(node *yaml.Node, field string, parsed durationStringer) string {
	if raw := resolveNode(mappingValue(node, field)); raw != nil && raw.Kind == yaml.ScalarNode {
		return raw.Value
	}
	if parsed == nil {
		return ""
	}
	value := parsed.String()
	if value == "0s" {
		return ""
	}
	return value
}

func rawOptionalDuration(node *yaml.Node, field string, parsed *prommodel.Duration) string {
	if raw := resolveNode(mappingValue(node, field)); raw != nil && raw.Kind == yaml.ScalarNode {
		return raw.Value
	}
	if parsed == nil {
		return ""
	}
	return parsed.String()
}

func copyMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	return maps.Clone(source)
}
