package rules

import (
	"fmt"
	"strings"
	"time"

	projectmodel "github.com/mansiverma897993/signoz/internal/model"
	prommodel "github.com/prometheus/common/model"
)

const (
	defaultEvalWindow    = time.Minute
	defaultEvalFrequency = time.Minute
	signozPromQLStep     = time.Minute
	minPointJitterBudget = 2
)

type evaluationPlan struct {
	EvalWindow       string
	Frequency        string
	Immediate        bool
	Safe             bool
	RequireMinPoints bool
	RequiredPoints   int
	Reasons          []projectmodel.ReasonCode
}

func evaluationSchedule(forValue, groupInterval string) evaluationPlan {
	if strings.TrimSpace(forValue) == "" {
		return immediateEvaluationPlan(groupInterval)
	}
	parsed, err := prommodel.ParseDuration(forValue)
	if err != nil || parsed < 0 {
		frequency, intervalSafe := evaluationFrequency(groupInterval, defaultEvalWindow)
		plan := evaluationPlan{
			EvalWindow: formatDuration(defaultEvalWindow), Frequency: frequency, Safe: false,
			Reasons: []projectmodel.ReasonCode{projectmodel.ReasonAlertForInvalid},
		}
		if !intervalSafe {
			plan.Reasons = append(plan.Reasons, projectmodel.ReasonRuleGroupInterval)
		}
		return plan
	}
	if parsed == 0 {
		return immediateEvaluationPlan(groupInterval)
	}
	window := time.Duration(parsed)
	frequency, intervalSafe := evaluationFrequency(groupInterval, window)
	plan := evaluationPlan{
		EvalWindow: formatDuration(window), Frequency: frequency, Safe: false,
		Reasons: []projectmodel.ReasonCode{projectmodel.ReasonAlertForWindow},
	}
	if !intervalSafe {
		plan.Reasons = append(plan.Reasons, projectmodel.ReasonRuleGroupInterval)
	}
	points := int(window/signozPromQLStep) - minPointJitterBudget
	if points > 0 {
		plan.RequireMinPoints = true
		plan.RequiredPoints = points
	}
	return plan
}

func immediateEvaluationPlan(groupInterval string) evaluationPlan {
	frequency, intervalSafe := evaluationFrequency(groupInterval, defaultEvalWindow)
	plan := evaluationPlan{
		EvalWindow: formatDuration(defaultEvalWindow), Frequency: frequency,
		Immediate: true, Safe: false,
		Reasons: []projectmodel.ReasonCode{projectmodel.ReasonAlertForDefault},
	}
	if !intervalSafe {
		plan.Reasons = append(plan.Reasons, projectmodel.ReasonRuleGroupInterval)
	}
	return plan
}

func evaluationFrequency(groupInterval string, window time.Duration) (string, bool) {
	value := min(window, defaultEvalFrequency)
	if strings.TrimSpace(groupInterval) == "" {
		return formatDuration(value), true
	}
	interval, err := prommodel.ParseDuration(groupInterval)
	if err != nil || interval < 0 || time.Duration(interval) > window {
		return formatDuration(value), false
	}
	if interval == 0 {
		return formatDuration(value), true
	}
	return formatDuration(time.Duration(interval)), true
}

func ruleGroupReasons(group projectmodel.RuleGroup) []projectmodel.ReasonCode {
	reasons := make([]projectmodel.ReasonCode, 0, 3)
	if value := strings.TrimSpace(group.QueryOffset); value != "" {
		offset, err := prommodel.ParseDuration(value)
		if err != nil || offset != 0 {
			reasons = append(reasons, projectmodel.ReasonRuleGroupQueryOffset)
		}
	}
	if group.Limit > 0 {
		reasons = append(reasons, projectmodel.ReasonRuleGroupLimit)
	}
	if value := strings.TrimSpace(group.Interval); value != "" {
		interval, err := prommodel.ParseDuration(value)
		if err != nil || interval < 0 {
			reasons = append(reasons, projectmodel.ReasonRuleGroupInterval)
		}
	}
	return reasons
}

func formatDuration(value time.Duration) string {
	if value%time.Hour == 0 {
		return fmt.Sprintf("%dh", value/time.Hour)
	}
	if value%time.Minute == 0 {
		return fmt.Sprintf("%dm", value/time.Minute)
	}
	if value%time.Second == 0 {
		return fmt.Sprintf("%ds", value/time.Second)
	}
	return value.String()
}

func isZeroDuration(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	duration, err := prommodel.ParseDuration(value)
	return err == nil && duration == 0
}
