package signoz

import "time"

// AlertRuleV2 is a SigNoz v2alpha1 threshold or PromQL rule payload.
type AlertRuleV2 struct {
	Alert                string                    `json:"alert"`
	AlertType            string                    `json:"alertType"`
	Description          string                    `json:"description,omitempty"`
	RuleType             string                    `json:"ruleType"`
	Version              string                    `json:"version"`
	SchemaVersion        string                    `json:"schemaVersion"`
	Condition            AlertConditionV2          `json:"condition"`
	Evaluation           AlertEvaluation           `json:"evaluation"`
	NotificationSettings AlertNotificationSettings `json:"notificationSettings"`
	Labels               map[string]string         `json:"labels"`
	Annotations          map[string]string         `json:"annotations,omitempty"`
	Disabled             bool                      `json:"disabled,omitempty"`
}

// AlertConditionV2 defines the selected query and its thresholds.
type AlertConditionV2 struct {
	CompositeQuery   AlertCompositeQuery `json:"compositeQuery"`
	SelectedQuery    string              `json:"selectedQueryName"`
	AlertOnAbsent    bool                `json:"alertOnAbsent,omitempty"`
	Thresholds       AlertThresholds     `json:"thresholds"`
	RequireMinPoints bool                `json:"requireMinPoints,omitempty"`
	RequiredPoints   int                 `json:"requiredNumPoints,omitempty"`
}

// AlertCompositeQuery contains strict v5 query envelopes used by a rule.
type AlertCompositeQuery struct {
	QueryType string               `json:"queryType"`
	PanelType string               `json:"panelType"`
	Unit      string               `json:"unit,omitempty"`
	Queries   []AlertQueryEnvelope `json:"queries"`
}

// AlertQueryEnvelope wraps one rule query.
type AlertQueryEnvelope struct {
	Type string         `json:"type"`
	Spec AlertQuerySpec `json:"spec"`
}

// AlertQuerySpec is the PromQL subset used by migrated Prometheus rules.
type AlertQuerySpec struct {
	Name     string `json:"name"`
	Query    string `json:"query"`
	Legend   string `json:"legend,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// AlertThresholds contains one or more threshold tiers.
type AlertThresholds struct {
	Kind string           `json:"kind"`
	Spec []AlertThreshold `json:"spec"`
}

// AlertThreshold is one target comparison and routing tier.
type AlertThreshold struct {
	Name           string   `json:"name"`
	Operator       string   `json:"op"`
	MatchType      string   `json:"matchType"`
	Target         float64  `json:"target"`
	RecoveryTarget *float64 `json:"recoveryTarget,omitempty"`
	Channels       []string `json:"channels,omitempty"`
}

// AlertEvaluation configures a rolling rule evaluation.
type AlertEvaluation struct {
	Kind string              `json:"kind"`
	Spec AlertEvaluationSpec `json:"spec"`
}

// AlertEvaluationSpec holds the window and cadence.
type AlertEvaluationSpec struct {
	EvalWindow string `json:"evalWindow"`
	Frequency  string `json:"frequency"`
}

// AlertNotificationSettings delegates source-label routing to target policies.
type AlertNotificationSettings struct {
	GroupBy   []string      `json:"groupBy,omitempty"`
	Renotify  AlertRenotify `json:"renotify"`
	UsePolicy bool          `json:"usePolicy"`
}

// AlertRenotify disables repeated notifications unless configured later.
type AlertRenotify struct {
	Enabled  bool   `json:"enabled"`
	Interval string `json:"interval,omitempty"`
}

// QueryRequestForAlert converts one PromQL alert into a strict v5 preflight request.
func QueryRequestForAlert(rule AlertRuleV2, now time.Time) QueryRangeRequest {
	queries := make([]QueryEnvelope, 0, len(rule.Condition.CompositeQuery.Queries))
	for _, query := range rule.Condition.CompositeQuery.Queries {
		queries = append(queries, QueryEnvelope{Type: query.Type, Spec: PromQLSpec{
			Name: query.Spec.Name, Query: query.Spec.Query, Disabled: query.Spec.Disabled, Step: 60, Stats: false, Legend: query.Spec.Legend,
		}})
	}
	return QueryRangeRequest{
		SchemaVersion:  "v1",
		Start:          uint64(now.Add(-time.Hour).UnixMilli()),
		End:            uint64(now.UnixMilli()),
		RequestType:    "time_series",
		CompositeQuery: CompositeQuery{Queries: queries},
		Variables:      map[string]VariableItem{},
		NoCache:        true,
	}
}
