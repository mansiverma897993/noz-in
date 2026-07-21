//go:build windows

package safeoutput

import "strings"

func platformPathEqual(left, right string) bool {
	return strings.EqualFold(left, right)
}
