package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mansiverma897993/signoz/internal/target/perses"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
)

// writeV6Sibling transforms the verified v5 dashboard into the Perses v6 shape
// and writes it next to the v5 file as <base>.v6.json. It is a secondary,
// opt-in output; the v5 file remains the verified primary import target.
func writeV6Sibling(dashboardPath string, payload signoz.DashboardV5) (string, error) {
	v6Path := strings.TrimSuffix(dashboardPath, ".signoz.json") + ".v6.json"
	data, err := json.MarshalIndent(perses.FromV5(payload), "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode v6 dashboard %q: %w", v6Path, err)
	}
	if err := os.WriteFile(v6Path, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("write v6 dashboard %q: %w", v6Path, err)
	}
	return v6Path, nil
}
