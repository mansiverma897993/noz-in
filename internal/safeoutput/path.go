// Package safeoutput prevents generated output from replacing an input or an
// authoritative artifact through lexical, symbolic-link, or hard-link aliases.
package safeoutput

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ProtectedPath describes one path that an output destination must not alias.
type ProtectedPath struct {
	Path    string
	Purpose string
}

// RejectAliases rejects destination when it names any protected path. Existing
// files are compared by identity so hard links are covered. Canonical paths are
// also compared after resolving every existing ancestor, which covers symlinked
// directories even when the final destination does not exist yet.
func RejectAliases(destination string, protected ...ProtectedPath) error {
	if destination == "" {
		return fmt.Errorf("output destination is empty")
	}
	for _, candidate := range protected {
		if candidate.Path == "" {
			continue
		}
		aliases, err := Aliases(destination, candidate.Path)
		if err != nil {
			return fmt.Errorf(
				"compare output destination %q with protected path %q: %w",
				destination,
				candidate.Path,
				err,
			)
		}
		if !aliases {
			continue
		}
		purpose := candidate.Purpose
		if purpose == "" {
			purpose = "authoritative artifact"
		}
		return fmt.Errorf(
			"refuse output destination %q: it aliases protected %s %q",
			destination,
			purpose,
			candidate.Path,
		)
	}
	return nil
}

// Aliases reports whether two paths resolve to the same filesystem object or
// canonical location. It supports paths whose final components do not exist.
func Aliases(left, right string) (bool, error) {
	if left == "" || right == "" {
		return false, nil
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo) {
		return true, nil
	}
	if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect %q: %w", left, leftErr)
	}
	if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect %q: %w", right, rightErr)
	}
	leftCanonical, err := canonicalPath(left)
	if err != nil {
		return false, err
	}
	rightCanonical, err := canonicalPath(right)
	if err != nil {
		return false, err
	}
	return platformPathEqual(leftCanonical, rightCanonical), nil
}

// LexicallyEqual reports whether two paths are the same cleaned absolute name.
// It deliberately does not follow links: only the designated output name, not
// an arbitrary alias to it, should receive special write semantics.
func LexicallyEqual(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && platformPathEqual(
		filepath.Clean(leftAbsolute),
		filepath.Clean(rightAbsolute),
	)
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path %q: %w", path, err)
	}
	current := filepath.Clean(absolute)
	var missing []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			parts := append([]string{resolved}, missing...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", fmt.Errorf("resolve path %q: %w", path, resolveErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve path %q: no existing ancestor", path)
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}
