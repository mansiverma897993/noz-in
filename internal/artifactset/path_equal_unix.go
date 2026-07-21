//go:build !windows

package artifactset

func platformPathEqual(left, right string) bool {
	return left == right
}
