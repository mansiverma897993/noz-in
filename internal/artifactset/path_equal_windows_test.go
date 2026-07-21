//go:build windows

package artifactset

import "testing"

func TestPlatformPathEqualUsesWindowsCaseFolding(t *testing.T) {
	t.Parallel()
	if !platformPathEqual(`C:\out\hosts.report.html`, `c:\OUT\HOSTS.REPORT.HTML`) {
		t.Fatal("Windows-equivalent artifact paths were treated as distinct")
	}
}
