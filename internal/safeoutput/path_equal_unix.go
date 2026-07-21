//go:build !windows

package safeoutput

func platformPathEqual(left, right string) bool {
	return left == right
}
