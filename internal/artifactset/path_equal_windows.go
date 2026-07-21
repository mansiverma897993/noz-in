//go:build windows

package artifactset

import "strings"

func platformPathEqual(left, right string) bool {
	return strings.EqualFold(left, right)
}
