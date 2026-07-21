package signoz

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRecommendedPromQLStepMatchesPinnedSigNozMetricPolicy(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		window time.Duration
		want   time.Duration
	}{
		{name: "short range minimum", window: time.Hour, want: time.Minute},
		{name: "one day five minute bucket", window: 24 * time.Hour, want: 5 * time.Minute},
		{name: "six days five minute rounding", window: 6 * 24 * time.Hour, want: 30 * time.Minute},
		{name: "seven days thirty minute rounding", window: 7 * 24 * time.Hour, want: 30 * time.Minute},
		{name: "thirty days", window: 30 * 24 * time.Hour, want: 150 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, RecommendedPromQLStep(test.window))
			assert.Equal(t, test.want, MinimumMetricStep(test.window))
		})
	}
}
