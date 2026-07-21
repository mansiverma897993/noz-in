package rules

import (
	"sort"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
)

func normalizeSeverity(source string) (string, bool, bool) {
	normalized := strings.ToLower(strings.TrimSpace(source))
	switch normalized {
	case "critical", "error", "warning", "info":
		return normalized, true, normalized != source
	case "warn":
		return "warning", true, true
	case "informational":
		return "info", true, true
	case "major":
		return "error", false, true
	case "minor":
		return "warning", false, true
	case "none":
		return "info", false, true
	default:
		return "warning", false, true
	}
}

func explicitTargetRuntimeLabels(expression string) []string {
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(expression)
	if err != nil {
		return nil
	}
	found := make(map[string]struct{})
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		switch typed := node.(type) {
		case *parser.VectorSelector:
			for _, matcher := range typed.LabelMatchers {
				if isExplicitTargetRuntimeLabel(matcher.Name) {
					found[matcher.Name] = struct{}{}
				}
			}
		case *parser.AggregateExpr:
			for _, label := range typed.Grouping {
				if isExplicitTargetRuntimeLabel(label) {
					found[label] = struct{}{}
				}
			}
			if typed.Op == parser.COUNT_VALUES {
				if label, ok := typed.Param.(*parser.StringLiteral); ok && isExplicitTargetRuntimeLabel(label.Val) {
					found[label.Val] = struct{}{}
				}
			}
		case *parser.BinaryExpr:
			if typed.VectorMatching == nil {
				break
			}
			for _, label := range typed.VectorMatching.MatchingLabels {
				if isExplicitTargetRuntimeLabel(label) {
					found[label] = struct{}{}
				}
			}
			for _, label := range typed.VectorMatching.Include {
				if isExplicitTargetRuntimeLabel(label) {
					found[label] = struct{}{}
				}
			}
		case *parser.Call:
			explicitRuntimeLabelFunctionArguments(typed, found)
		}
		return nil
	})
	result := make([]string, 0, len(found))
	for label := range found {
		result = append(result, label)
	}
	sort.Strings(result)
	return result
}

func explicitRuntimeLabelFunctionArguments(call *parser.Call, found map[string]struct{}) {
	if call == nil || call.Func == nil {
		return
	}
	indices := []int(nil)
	switch call.Func.Name {
	case "label_replace":
		indices = []int{1, 3}
	case "label_join":
		for index := 1; index < len(call.Args); index++ {
			if index != 2 {
				indices = append(indices, index)
			}
		}
	case "sort_by_label", "sort_by_label_desc":
		for index := 1; index < len(call.Args); index++ {
			indices = append(indices, index)
		}
	}
	for _, index := range indices {
		if index >= len(call.Args) {
			continue
		}
		label, ok := call.Args[index].(*parser.StringLiteral)
		if ok && isExplicitTargetRuntimeLabel(label.Val) {
			found[label.Val] = struct{}{}
		}
	}
}

func isExplicitTargetRuntimeLabel(label string) bool {
	return label == "severity" || label == "threshold.name" || label == "alertname"
}

func mergeSortedStrings(collections ...[]string) []string {
	set := make(map[string]struct{})
	for _, collection := range collections {
		for _, value := range collection {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func remapConfiguredAlertLabels(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]string, len(source))
	for _, key := range keys {
		result[targetLabel(key)] = source[key]
	}
	return result
}

func targetLabel(source string) string {
	switch source {
	case "job":
		return "service.name"
	case "instance":
		return "service.instance.id"
	default:
		return source
	}
}

func validSigNozRuleLabelName(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	if first != '_' && (first < 'a' || first > 'z') && (first < 'A' || first > 'Z') {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character == '_' || character == '.' ||
			character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}
