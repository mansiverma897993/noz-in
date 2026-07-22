package validate

import (
	"math"
	"sort"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// executionSamplePointLimit bounds how many points each retained series
// carries into the evidence report. Longer series are downsampled evenly so
// the chart keeps the overall shape without inflating the artifact.
const executionSamplePointLimit = 100

// sampleSeries converts the client's bounded execution sample into the report
// representation: labels compacted deterministically, non-finite values
// dropped (report JSON must stay strict), and points evenly downsampled.
func sampleSeries(sample []signoz.MetricSeries) []reporttypes.SeriesSample {
	if len(sample) == 0 {
		return nil
	}
	converted := make([]reporttypes.SeriesSample, 0, len(sample))
	for _, series := range sample {
		points := make([]reporttypes.SamplePoint, 0, len(series.Values))
		for _, value := range series.Values {
			if math.IsNaN(value.Value) || math.IsInf(value.Value, 0) {
				continue
			}
			points = append(points, reporttypes.SamplePoint{Timestamp: value.Timestamp, Value: value.Value})
		}
		if len(points) == 0 {
			continue
		}
		sort.Slice(points, func(left, right int) bool { return points[left].Timestamp < points[right].Timestamp })
		converted = append(converted, reporttypes.SeriesSample{
			Labels: compactLabels(series.Labels),
			Points: downsamplePoints(points, executionSamplePointLimit),
		})
	}
	if len(converted) == 0 {
		return nil
	}
	return converted
}

func compactLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ", ")
}

func downsamplePoints(points []reporttypes.SamplePoint, limit int) []reporttypes.SamplePoint {
	if limit <= 0 || len(points) <= limit {
		return points
	}
	sampled := make([]reporttypes.SamplePoint, 0, limit)
	step := float64(len(points)-1) / float64(limit-1)
	for index := range limit {
		sampled = append(sampled, points[int(math.Round(float64(index)*step))])
	}
	return sampled
}
