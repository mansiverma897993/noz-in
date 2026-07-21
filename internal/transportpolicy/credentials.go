// Package transportpolicy centralizes credential-bearing HTTP policy.
package transportpolicy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// RequireProtectedCredentials rejects credentials over plaintext HTTP unless
// the endpoint is a literal loopback origin or the caller explicitly opted in.
func RequireProtectedCredentials(endpoint *url.URL, credentialPresent, allowInsecure bool, service string) error {
	if endpoint == nil || endpoint.Scheme != "http" || !credentialPresent || allowInsecure {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	if host == "localhost" {
		return nil
	}
	if address := net.ParseIP(host); address != nil && address.IsLoopback() {
		return nil
	}
	return fmt.Errorf(
		"%s URL uses plaintext HTTP with credentials; use HTTPS or explicitly allow insecure HTTP",
		service,
	)
}
