package app

import "github.com/mansiverma897993/signoz/internal/safeoutput"

// ProtectedInputPath identifies a file-backed input that generated artifacts
// must never replace through a lexical path, symlink, or hardlink alias.
// Callers use this for inputs already decoded before the migration package is
// invoked, such as API-key files and metric-name maps.
type ProtectedInputPath struct {
	Path    string
	Purpose string
}

func appendProtectedInputs(
	destination []safeoutput.ProtectedPath,
	inputs []ProtectedInputPath,
) []safeoutput.ProtectedPath {
	for _, input := range inputs {
		if input.Path == "" {
			continue
		}
		purpose := input.Purpose
		if purpose == "" {
			purpose = "migration input"
		}
		destination = append(destination, safeoutput.ProtectedPath{Path: input.Path, Purpose: purpose})
	}
	return destination
}
