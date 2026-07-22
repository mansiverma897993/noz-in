package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/app"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationResponseUsesDataPresenceVocabulary(t *testing.T) {
	t.Parallel()

	response := migrationResponse("migration-id", app.GrafanaResult{
		Summary:  reporttypes.Summary{DataPresentPercent: 75},
		Evidence: reporttypes.Report{Dashboard: reporttypes.DashboardInfo{Title: "Presence"}},
	}, false)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"data_present_pct":75`)
	assert.NotContains(t, string(encoded), "data_verified")
}

func TestValidationResponseUsesDataPresenceVocabulary(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(validateResponse{
		Delta:       validationDelta{NewDataPresent: 1, DataNoLongerPresent: 2},
		NoData:      []validationFailure{{ErrorCode: "NO_DATA_RETURNED"}},
		NoDataTotal: 1,
	})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"new_data_present":1`)
	assert.Contains(t, string(encoded), `"data_no_longer_present":2`)
	assert.Contains(t, string(encoded), `"no_data_total":1`)
	assert.NotContains(t, string(encoded), "verified")
}
