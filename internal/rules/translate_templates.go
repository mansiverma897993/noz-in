package rules

import (
	"fmt"
	"sort"
	"strings"
	texttemplate "text/template"
	templateparse "text/template/parse"
)

var targetMutatedTemplateLabels = map[string]struct{}{
	"severity":       {},
	"threshold.name": {},
}

const unsupportedPrometheusTemplateSentinel = "[unsupported Prometheus template omitted]"

var unsupportedPrometheusTemplateFunctions = map[string]struct{}{
	"query": {}, "graphLink": {}, "tableLink": {}, "stripPort": {},
	"stripDomain": {}, "humanizePercentage": {}, "toTime": {},
	"toDuration": {}, "now": {}, "parseDuration": {},
	"urlQueryEscape": {}, "pathPrefix": {}, "externalURL": {},
}

var prometheusTemplateParseFunctions = newPrometheusTemplateParseFunctions()

const prometheusTemplateDefinitions = `{{$labels := .Labels}}{{$externalLabels := .ExternalLabels}}{{$externalURL := .ExternalURL}}{{$value := .Value}}`

func newPrometheusTemplateParseFunctions() texttemplate.FuncMap {
	dummy := func(...any) any { return nil }
	result := make(texttemplate.FuncMap)
	for _, name := range []string{
		"query", "first", "label", "value", "strvalue", "args",
		"reReplaceAll", "safeHtml", "match", "title", "toUpper", "toLower",
		"graphLink", "tableLink", "sortByLabel", "stripPort", "stripDomain",
		"humanize", "humanize1024", "humanizeDuration", "humanizePercentage",
		"humanizeTimestamp", "toTime", "toDuration", "now", "pathPrefix",
		"externalURL", "parseDuration", "urlQueryEscape",
	} {
		result[name] = dummy
	}
	return result
}

func rewriteAlertTemplates(source map[string]string) (map[string]string, []string, bool, bool) {
	if len(source) == 0 {
		return nil, nil, false, false
	}
	result := make(map[string]string, len(source))
	groups := make(map[string]struct{})
	formattingDropped := false
	unsupported := false
	for key, value := range source {
		var rewritten strings.Builder
		position := 0
		for position < len(value) {
			relativeStart := strings.Index(value[position:], "{{")
			if relativeStart < 0 {
				rewritten.WriteString(escapeSigNozBareDollar(value[position:]))
				break
			}
			start := position + relativeStart
			rewritten.WriteString(escapeSigNozBareDollar(value[position:start]))
			end, ok := findAlertTemplateActionEnd(value, start+2)
			if !ok {
				rewritten.WriteString(unsupportedPrometheusTemplateSentinel)
				unsupported = true
				break
			}
			replacement, label, dropped, unsupportedAction := rewriteAlertTemplateAction(value[start:end])
			rewritten.WriteString(replacement)
			if label != "" {
				groups[label] = struct{}{}
			}
			formattingDropped = formattingDropped || dropped
			unsupported = unsupported || unsupportedAction
			position = end
		}
		result[key] = rewritten.String()
	}
	groupBy := make([]string, 0, len(groups))
	for name := range groups {
		groupBy = append(groupBy, name)
	}
	sort.Strings(groupBy)
	return result, groupBy, formattingDropped, unsupported
}

func rewriteAlertTemplateAction(action string) (string, string, bool, bool) {
	if len(action) < 4 || !strings.HasPrefix(action, "{{") || !strings.HasSuffix(action, "}}") {
		return unsupportedPrometheusTemplateSentinel, "", false, true
	}
	body, trimmed := alertTemplateActionBody(action)
	if strings.HasPrefix(body, "/*") && strings.HasSuffix(body, "*/") {
		if trimmed {
			return unsupportedPrometheusTemplateSentinel, "", false, true
		}
		return "", "", false, false
	}
	analysis, err := analyzePrometheusTemplateAction(action)
	if err != nil || analysis.unsupported {
		return unsupportedPrometheusTemplateSentinel, "", false, true
	}
	if analysis.hasValue && len(analysis.labels) > 0 || len(analysis.labels) > 1 {
		return unsupportedPrometheusTemplateSentinel, "", false, true
	}
	if analysis.hasValue {
		return "{{$value}}", "", trimmed || !analysis.directValue, false
	}
	if len(analysis.labels) == 1 {
		target := targetLabel(analysis.labels[0])
		if !validSigNozRuleLabelName(target) {
			return unsupportedPrometheusTemplateSentinel, "", false, true
		}
		if _, mutated := targetMutatedTemplateLabels[target]; mutated {
			return unsupportedPrometheusTemplateSentinel, "", false, true
		}
		return "{{$" + target + "}}", target, trimmed || analysis.directLabel != analysis.labels[0], false
	}

	// SigNoz v0.133 declares externalURL and pathPrefix functions, but the
	// Prometheus-rule execution path constructs the template expander with a nil
	// URL. Treat both source variables and target-looking calls as unsupported;
	// parse-only acceptance would still fail when the alert renders.
	return unsupportedPrometheusTemplateSentinel, "", false, true
}

func alertTemplateActionBody(action string) (string, bool) {
	body := action[2 : len(action)-2]
	trimmed := false
	if len(body) >= 2 && body[0] == '-' && isTemplateSpace(body[1]) {
		body = body[1:]
		trimmed = true
	}
	if len(body) >= 2 && body[len(body)-1] == '-' && isTemplateSpace(body[len(body)-2]) {
		body = body[:len(body)-1]
		trimmed = true
	}
	return strings.TrimSpace(body), trimmed
}

type prometheusTemplateAnalysis struct {
	labels      []string
	hasValue    bool
	unsupported bool
	directLabel string
	directValue bool
}

func analyzePrometheusTemplateAction(action string) (prometheusTemplateAnalysis, error) {
	template, err := texttemplate.New("source-alert-template").Funcs(prometheusTemplateParseFunctions).Parse(
		prometheusTemplateDefinitions + action,
	)
	if err != nil {
		return prometheusTemplateAnalysis{}, err
	}
	const definitionNodes = 4
	if template.Tree == nil || template.Root == nil || len(template.Root.Nodes) != definitionNodes+1 {
		return prometheusTemplateAnalysis{}, fmt.Errorf("source alert template action did not produce one executable node")
	}
	actionNode, ok := template.Root.Nodes[definitionNodes].(*templateparse.ActionNode)
	if !ok {
		return prometheusTemplateAnalysis{}, fmt.Errorf("source alert template action is not a simple executable action")
	}
	labels := make(map[string]struct{})
	analysis := prometheusTemplateAnalysis{}
	inspectPrometheusTemplateNode(actionNode.Pipe, labels, &analysis)
	for label := range labels {
		analysis.labels = append(analysis.labels, label)
	}
	sort.Strings(analysis.labels)
	analysis.directLabel, _ = directPrometheusTemplateLabel(actionNode)
	analysis.directValue = directPrometheusTemplateValue(actionNode)
	return analysis, nil
}

func inspectPrometheusTemplateNode(
	node templateparse.Node,
	labels map[string]struct{},
	analysis *prometheusTemplateAnalysis,
) {
	switch typed := node.(type) {
	case *templateparse.PipeNode:
		for _, command := range typed.Cmds {
			inspectPrometheusTemplateNode(command, labels, analysis)
		}
	case *templateparse.CommandNode:
		if label, ok := indexedPrometheusTemplateLabel(typed); ok {
			labels[label] = struct{}{}
			return
		}
		for _, argument := range typed.Args {
			inspectPrometheusTemplateNode(argument, labels, analysis)
		}
	case *templateparse.VariableNode:
		switch {
		case len(typed.Ident) == 1 && typed.Ident[0] == "$value":
			analysis.hasValue = true
		case len(typed.Ident) == 2 && typed.Ident[0] == "$labels":
			labels[typed.Ident[1]] = struct{}{}
		default:
			analysis.unsupported = true
		}
	case *templateparse.FieldNode:
		switch {
		case len(typed.Ident) == 1 && typed.Ident[0] == "Value":
			analysis.hasValue = true
		case len(typed.Ident) == 2 && typed.Ident[0] == "Labels":
			labels[typed.Ident[1]] = struct{}{}
		default:
			analysis.unsupported = true
		}
	case *templateparse.IdentifierNode:
		if _, unsupported := unsupportedPrometheusTemplateFunctions[typed.Ident]; unsupported {
			analysis.unsupported = true
		}
	case *templateparse.ChainNode:
		analysis.unsupported = true
		inspectPrometheusTemplateNode(typed.Node, labels, analysis)
	case *templateparse.DotNode, *templateparse.TemplateNode:
		analysis.unsupported = true
	}
}

func indexedPrometheusTemplateLabel(command *templateparse.CommandNode) (string, bool) {
	if command == nil || len(command.Args) != 3 {
		return "", false
	}
	identifier, ok := command.Args[0].(*templateparse.IdentifierNode)
	if !ok || identifier.Ident != "index" || !prometheusTemplateLabelsMap(command.Args[1]) {
		return "", false
	}
	label, ok := command.Args[2].(*templateparse.StringNode)
	if !ok {
		return "", false
	}
	return label.Text, true
}

func prometheusTemplateLabelsMap(node templateparse.Node) bool {
	switch typed := node.(type) {
	case *templateparse.VariableNode:
		return len(typed.Ident) == 1 && typed.Ident[0] == "$labels"
	case *templateparse.FieldNode:
		return len(typed.Ident) == 1 && typed.Ident[0] == "Labels"
	default:
		return false
	}
}

func directPrometheusTemplateLabel(action *templateparse.ActionNode) (string, bool) {
	if action == nil || action.Pipe == nil || len(action.Pipe.Decl) > 0 || len(action.Pipe.Cmds) != 1 {
		return "", false
	}
	command := action.Pipe.Cmds[0]
	if label, ok := indexedPrometheusTemplateLabel(command); ok {
		return label, true
	}
	if len(command.Args) != 1 {
		return "", false
	}
	switch typed := command.Args[0].(type) {
	case *templateparse.VariableNode:
		if len(typed.Ident) == 2 && typed.Ident[0] == "$labels" {
			return typed.Ident[1], true
		}
	case *templateparse.FieldNode:
		if len(typed.Ident) == 2 && typed.Ident[0] == "Labels" {
			return typed.Ident[1], true
		}
	}
	return "", false
}

func directPrometheusTemplateValue(action *templateparse.ActionNode) bool {
	if action == nil || action.Pipe == nil || len(action.Pipe.Decl) > 0 || len(action.Pipe.Cmds) != 1 {
		return false
	}
	command := action.Pipe.Cmds[0]
	if len(command.Args) != 1 {
		return false
	}
	switch typed := command.Args[0].(type) {
	case *templateparse.VariableNode:
		return len(typed.Ident) == 1 && typed.Ident[0] == "$value"
	case *templateparse.FieldNode:
		return len(typed.Ident) == 1 && typed.Ident[0] == "Value"
	default:
		return false
	}
}

func findAlertTemplateActionEnd(value string, start int) (int, bool) {
	var quote byte
	escaped := false
	comment := false
	for index := start; index < len(value); index++ {
		character := value[index]
		if comment {
			if character == '*' && index+1 < len(value) && value[index+1] == '/' {
				comment = false
				index++
			}
			continue
		}
		if quote != 0 {
			if quote != '`' && escaped {
				escaped = false
				continue
			}
			if quote != '`' && character == '\\' {
				escaped = true
				continue
			}
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '/' && index+1 < len(value) && value[index+1] == '*' {
			comment = true
			index++
			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character
			continue
		}
		if character == '}' && index+1 < len(value) && value[index+1] == '}' {
			return index + 2, true
		}
	}
	return 0, false
}

func isTemplateSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func escapeSigNozBareDollar(text string) string {
	return strings.ReplaceAll(text, "$", `{{"$"}}`)
}
