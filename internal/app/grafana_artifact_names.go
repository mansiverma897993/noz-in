package app

// Portable artifact base-name derivation and collision-free reservation of the
// per-input artifact name sets.

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func artifactBase(path string) string {
	raw := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if raw == "" || raw == "." {
		return "dashboard"
	}
	if portableArtifactBase(raw) {
		return raw
	}
	digest := sha256.Sum256([]byte(raw))
	base := portableArtifactSlug(raw)
	if base == "" || windowsReservedArtifactName(base) {
		base = "artifact"
	}
	return fmt.Sprintf("%s-%x", base, digest[:8])
}

const maxArtifactBaseBytes = 120

func portableArtifactBase(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > maxArtifactBaseBytes ||
		strings.TrimRight(value, " .") != value || windowsReservedArtifactName(value) {
		return false
	}
	for _, character := range value {
		allowed := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._-", character)
		if character > 127 || character < 32 || !allowed {
			return false
		}
	}
	return true
}

func portableArtifactSlug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		allowed := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._-", character)
		if !allowed {
			character = '-'
		}
		if character == '-' && lastDash {
			continue
		}
		if builder.Len()+1 > maxArtifactBaseBytes-17 {
			break
		}
		builder.WriteRune(character)
		lastDash = character == '-'
	}
	return strings.Trim(builder.String(), " .-")
}

func windowsReservedArtifactName(value string) bool {
	stem := strings.ToUpper(strings.TrimRight(strings.SplitN(value, ".", 2)[0], " ."))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$":
		return true
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
		stem[3] >= '1' && stem[3] <= '9' {
		return true
	}
	return false
}

func artifactBases(paths []string) []string {
	// Reserve every artifact a base may produce, including the optional
	// candidate dashboard. Looking only for duplicate source basenames is not
	// sufficient: "foo.json" and "foo.candidate.json" would otherwise both
	// publish foo.candidate.signoz.json. Every participant in a natural-name
	// collision receives a path digest so argument order cannot decide which
	// source gets the unsuffixed artifact. The final collision loop protects
	// repeated paths, adversarial filenames, and equal digest prefixes.
	naturalOwners := make(map[string][]int, len(paths)*4)
	for index, path := range paths {
		for _, name := range artifactNames(artifactBase(path)) {
			key := artifactKey(name)
			naturalOwners[key] = append(naturalOwners[key], index)
		}
	}
	forceDigest := make([]bool, len(paths))
	for _, owners := range naturalOwners {
		if len(owners) < 2 {
			continue
		}
		for _, index := range owners {
			forceDigest[index] = true
		}
	}

	initial := make([]string, len(paths))
	for index, path := range paths {
		base := artifactBase(path)
		if forceDigest[index] {
			digest := sha256.Sum256([]byte(filepath.Clean(path)))
			base = fmt.Sprintf("%s-%x", base, digest[:8])
		}
		initial[index] = base
	}
	order := make([]int, len(paths))
	for index := range paths {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		leftPath := filepath.Clean(paths[order[left]])
		rightPath := filepath.Clean(paths[order[right]])
		if leftPath == rightPath {
			return order[left] < order[right]
		}
		return leftPath < rightPath
	})

	usedArtifacts := make(map[string]bool, len(paths)*4)
	result := make([]string, len(paths))
	for _, index := range order {
		base := initial[index]
		root := base
		for ordinal := 2; artifactBaseCollides(base, usedArtifacts); ordinal++ {
			base = fmt.Sprintf("%s-%d", root, ordinal)
		}
		for _, name := range artifactNames(base) {
			usedArtifacts[artifactKey(name)] = true
		}
		result[index] = base
	}
	return result
}

func artifactBaseCollides(base string, used map[string]bool) bool {
	for _, name := range artifactNames(base) {
		if used[artifactKey(name)] {
			return true
		}
	}
	return false
}

func artifactKey(name string) string {
	return cases.Fold().String(norm.NFC.String(name))
}

func artifactNames(base string) []string {
	return append(dashboardArtifactNames(base), ruleArtifactNames(base)...)
}

func dashboardArtifactNames(base string) []string {
	return []string{
		base + ".signoz.json",
		base + ".candidate.signoz.json",
		base + ".report.json",
		base + ".report.html",
		base + ".artifacts.json",
	}
}

func ruleArtifactNames(base string) []string {
	return []string{
		base + ".signoz-rules.json",
		base + ".rules-report.json",
		base + ".rules-report.html",
		base + ".rules-artifacts.json",
	}
}
