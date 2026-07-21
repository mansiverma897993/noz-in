//go:build windows

package safeoutput

import "testing"

func TestPlatformPathEqualUsesWindowsCaseFolding(t *testing.T) {
	t.Parallel()
	if !platformPathEqual(`C:\out\hosts.report.html`, `c:\OUT\HOSTS.REPORT.HTML`) {
		t.Fatal("Windows-equivalent output paths were treated as distinct")
	}
}
