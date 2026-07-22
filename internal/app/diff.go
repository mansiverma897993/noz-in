package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mansiverma897993/noz-in/internal/diff"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// DifferentialOptions controls live Prometheus-to-SigNoz semantic validation.
type DifferentialOptions struct {
	SourceURL         string
	SourceBearerToken string
	TargetURL         string
	TargetAPIKey      string
	// TargetProvenance is an explicit operator assertion about the target
	// ingestion path. An empty value keeps exact label matching and enables no
	// target-only label exceptions.
	TargetProvenance     string
	HTTPClient           *http.Client
	AllowInsecureHTTP    bool
	SourceVariables      map[string]string
	TargetVariables      map[string]string
	MetricNameMap        map[string]string
	RateInterval         time.Duration
	Interval             time.Duration
	Range                time.Duration
	Step                 time.Duration
	TimestampTolerance   time.Duration
	RelativeTolerance    float64
	AbsoluteTolerance    float64
	MinimumCoverage      float64
	MinimumMatchedPoints int
	Workers              int
	MaxQueries           int
	Now                  time.Time
	MigrationReportPath  string
}

// DifferentialWindow records the exact comparison interval.
type DifferentialWindow struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	StepMillis int64     `json:"stepMillis"`
}

// DifferentialTolerances records the acceptance thresholds used by the comparator.
type DifferentialTolerances struct {
	TimestampMillis      int64   `json:"timestampMillis"`
	Relative             float64 `json:"relative"`
	Absolute             float64 `json:"absolute"`
	Coverage             float64 `json:"coverage"`
	MinimumMatchedPoints int     `json:"minimumMatchedPoints"`
}

// DifferentialLabelValueAliasBinding records the exact variable and source /
// target values whose query-local proof authorized one label-value alias.
type DifferentialLabelValueAliasBinding struct {
	VariableName string `json:"variableName"`
	SourceLabel  string `json:"sourceLabel"`
	TargetLabel  string `json:"targetLabel"`
	SourceValue  string `json:"sourceValue"`
	TargetValue  string `json:"targetValue"`
}

// DifferentialQuery records one query pair and its measured outcome.
type DifferentialQuery struct {
	PanelTitle              string                               `json:"panelTitle"`
	RefID                   string                               `json:"refId"`
	SourcePath              string                               `json:"sourcePath"`
	Verdict                 model.Verdict                        `json:"verdict"`
	Reasons                 []model.ReasonCode                   `json:"reasons,omitempty"`
	SourceExpression        string                               `json:"sourceExpression,omitempty"`
	TargetExpression        string                               `json:"targetExpression,omitempty"`
	TargetKind              string                               `json:"targetKind,omitempty"`
	TargetQueryName         string                               `json:"targetQueryName,omitempty"`
	TargetSpecSHA256        string                               `json:"targetSpecSha256,omitempty"`
	TargetArtifact          json.RawMessage                      `json:"targetArtifact,omitempty"`
	TargetArtifactSHA256    string                               `json:"targetArtifactSha256,omitempty"`
	EvaluationStepMillis    int64                                `json:"evaluationStepMillis,omitempty"`
	Window                  *DifferentialWindow                  `json:"window,omitempty"`
	MissingSource           []string                             `json:"missingSourceVariables,omitempty"`
	MissingTarget           []string                             `json:"missingTargetVariables,omitempty"`
	LabelValueAliases       map[string]map[string]string         `json:"labelValueAliases,omitempty"`
	LabelValueAliasBindings []DifferentialLabelValueAliasBinding `json:"labelValueAliasBindings,omitempty"`
	Stats                   diff.Stats                           `json:"stats"`
	Error                   string                               `json:"error,omitempty"`
	SkippedReason           string                               `json:"skippedReason,omitempty"`
}

// DifferentialSummary counts every comparison outcome.
type DifferentialSummary struct {
	Queries             int `json:"queries"`
	Compared            int `json:"compared"`
	Equivalent          int `json:"equivalent"`
	ValueMismatch       int `json:"valueMismatch"`
	InsufficientOverlap int `json:"insufficientOverlap"`
	NoSourceData        int `json:"noSourceData"`
	NoTargetData        int `json:"noTargetData"`
	BothEmpty           int `json:"bothEmpty"`
	TargetOnlyData      int `json:"targetOnlyData"`
	NoSeriesMatch       int `json:"noSeriesMatch"`
	Errors              int `json:"errors"`
	Skipped             int `json:"skipped"`
}

// DifferentialReport is a reproducible source/target evidence artifact.
type DifferentialReport struct {
	Source            model.Source                            `json:"source"`
	SourceURL         string                                  `json:"sourceUrl"`
	TargetURL         string                                  `json:"targetUrl"`
	TargetProvenance  diff.TargetProvenance                   `json:"targetProvenance"`
	AllowInsecureHTTP bool                                    `json:"allowInsecureHttp"`
	PrimaryArtifact   *reporttypes.ArtifactBinding            `json:"primaryArtifact,omitempty"`
	Materialization   reporttypes.DifferentialMaterialization `json:"materialization"`
	Window            DifferentialWindow                      `json:"window"`
	Tolerances        DifferentialTolerances                  `json:"tolerances"`
	Summary           DifferentialSummary                     `json:"summary"`
	Comparisons       []DifferentialQuery                     `json:"comparisons"`
}

type differentialTask struct {
	index             int
	sourceExpression  string
	targetRequest     signoz.QueryRangeRequest
	targetQueryName   string
	targetKind        diff.TargetKind
	step              time.Duration
	window            DifferentialWindow
	labelValueAliases map[string]map[string]string
}

const maxMigrationReportSize = 64 << 20

// ValidateGrafanaDifferential executes equivalent source and target PromQL and compares the samples.
func ValidateGrafanaDifferential(ctx context.Context, path string, options DifferentialOptions) (DifferentialReport, error) {
	options, compareOptions, err := normalizeDifferentialOptions(options)
	if err != nil {
		return DifferentialReport{}, inputError(err)
	}
	runtime, err := prepareDifferentialRuntime(ctx, path, options, compareOptions)
	if err != nil {
		return DifferentialReport{}, err
	}

	report := newDifferentialReport(
		runtime.dashboard.Source,
		options,
		runtime.primaryArtifact,
		runtime.materialization,
	)
	tasks, err := planDifferentialQueries(&report, runtime, options)
	if err != nil {
		return DifferentialReport{}, err
	}
	executeDifferentialTasks(ctx, report.Comparisons, tasks, runtime, options.Workers)
	report.Summary = summarizeDifferential(report.Comparisons)
	return report, nil
}

func summarizeDifferential(comparisons []DifferentialQuery) DifferentialSummary {
	summary := DifferentialSummary{Queries: len(comparisons)}
	for _, comparison := range comparisons {
		switch comparison.Stats.Status {
		case diff.StatusEquivalent:
			summary.Compared++
			summary.Equivalent++
		case diff.StatusValueMismatch:
			summary.Compared++
			summary.ValueMismatch++
		case diff.StatusInsufficientOverlap:
			summary.Compared++
			summary.InsufficientOverlap++
		case diff.StatusNoSourceData:
			summary.Compared++
			summary.NoSourceData++
		case diff.StatusNoTargetData:
			summary.Compared++
			summary.NoTargetData++
		case diff.StatusBothEmpty:
			summary.Compared++
			summary.BothEmpty++
		case diff.StatusTargetOnlyData:
			summary.Compared++
			summary.TargetOnlyData++
		case diff.StatusNoSeriesMatch:
			summary.Compared++
			summary.NoSeriesMatch++
		case diff.StatusError:
			summary.Errors++
		case diff.StatusSkipped:
			summary.Skipped++
		}
	}
	return summary
}

func defaultCoverage(value float64) float64 {
	if value <= 0 || value > 1 {
		return 0.8
	}
	return value
}

func defaultMinimumMatchedPoints(value int) int {
	if value <= 0 {
		return 10
	}
	return value
}

// WriteDifferentialReport writes a formatted JSON report.
func WriteDifferentialReport(path string, report DifferentialReport) error {
	return writeJSON(path, report)
}
