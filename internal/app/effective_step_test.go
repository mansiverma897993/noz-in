package app

import (
	"testing"
	"time"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/stretchr/testify/assert"
)

func TestBuilderDifferentialStepIncludesPinnedBackendMetricClamp(t *testing.T) {
	t.Parallel()

	widget := signoz.Widget{PanelTypes: "graph", Query: signoz.WidgetQuery{
		QueryType: "builder",
		Builder: signoz.BuilderContainer{
			QueryData: []signoz.BuilderQueryData{{
				QueryName: "A_1", StepInterval: 60,
				Aggregations: []signoz.MetricAggregation{{
					MetricName: "up", TimeAggregation: "latest", SpaceAggregation: "sum",
				}},
			}},
			QueryFormulas: []signoz.BuilderFormula{{QueryName: "A", Expression: "A_1"}},
		},
	}}
	options := DifferentialOptions{
		Now: time.Unix(1_800_000_000, 0), Range: 24 * time.Hour, Step: time.Minute,
	}

	queryStep, _ := differentialQueryWindow(widget, "A_1", options)
	formulaStep, _ := differentialQueryWindow(widget, "A", options)
	assert.Equal(t, 5*time.Minute, queryStep)
	assert.Equal(t, 5*time.Minute, formulaStep)
}

func TestBuilderDifferentialStepStabilizesAgainstAlignedRequestWindow(t *testing.T) {
	t.Parallel()

	widget := signoz.Widget{PanelTypes: "graph", Query: signoz.WidgetQuery{
		QueryType: "builder",
		Builder: signoz.BuilderContainer{QueryData: []signoz.BuilderQueryData{{
			QueryName: "A", StepInterval: 60,
		}}},
	}}
	step, window := differentialQueryWindow(widget, "A", DifferentialOptions{
		Now: time.Unix(1_800_000_000, 0), Range: 24*time.Hour - time.Second, Step: time.Minute,
	})
	assert.Equal(t, 5*time.Minute, step)
	assert.Equal(t, 24*time.Hour, window.End.Sub(window.Start))
}

func TestBuilderFormulaStepUsesReferencedDependencyGCD(t *testing.T) {
	t.Parallel()

	builder := signoz.BuilderContainer{QueryData: []signoz.BuilderQueryData{
		{QueryName: "A_1", StepInterval: 120},
		{QueryName: "B_1", StepInterval: 180},
		{QueryName: "unused", StepInterval: 900},
	}}
	assert.Equal(
		t, time.Minute,
		effectiveBuilderFormulaStep(builder, "A_1.0 / B_1", 12*time.Hour),
	)
	assert.Equal(
		t, 2*time.Minute,
		effectiveBuilderFormulaStep(builder, "A_1", 12*time.Hour),
	)
	assert.Equal(
		t, time.Minute,
		effectiveBuilderFormulaStep(builder, "missing + 1", 24*time.Hour),
	)
}
