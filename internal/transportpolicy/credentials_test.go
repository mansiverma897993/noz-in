package transportpolicy

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireProtectedCredentials(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		endpoint   string
		credential bool
		allow      bool
		wantError  bool
	}{
		{name: "https", endpoint: "https://example.com", credential: true},
		{name: "localhost", endpoint: "http://localhost:8080", credential: true},
		{name: "ipv4 loopback", endpoint: "http://127.0.0.1:8080", credential: true},
		{name: "ipv6 loopback", endpoint: "http://[::1]:8080", credential: true},
		{name: "no credential", endpoint: "http://10.0.0.2:9090", credential: false},
		{name: "explicit opt in", endpoint: "http://10.0.0.2:8080", credential: true, allow: true},
		{name: "private network is not implicit", endpoint: "http://10.0.0.2:8080", credential: true, wantError: true},
		{name: "public network", endpoint: "http://example.com", credential: true, wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			endpoint, err := url.Parse(testCase.endpoint)
			require.NoError(t, err)
			err = RequireProtectedCredentials(endpoint, testCase.credential, testCase.allow, "test")
			if testCase.wantError {
				require.ErrorContains(t, err, "plaintext HTTP")
				return
			}
			require.NoError(t, err)
		})
	}
}
