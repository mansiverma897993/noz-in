package model

import (
	"os"
	"strings"
	"testing"
)

func TestReasonCodesHaveDescriptions(t *testing.T) {
	t.Parallel()

	codes := ReasonCodes()
	if len(codes) != len(reasonDescriptions) {
		t.Fatalf("ReasonCodes returned %d codes for %d descriptions", len(codes), len(reasonDescriptions))
	}

	seen := make(map[ReasonCode]struct{}, len(codes))
	for _, code := range codes {
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("duplicate reason code %q", code)
		}
		seen[code] = struct{}{}

		description, ok := ReasonDescription(code)
		if !ok || description == "" {
			t.Errorf("reason code %q has no description", code)
		}
	}
}

func TestReasonCodeDocumentationIsComplete(t *testing.T) {
	t.Parallel()

	documentation, err := os.ReadFile("../../docs/reason-codes.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range ReasonCodes() {
		needle := "`" + string(code) + "`"
		if !strings.Contains(string(documentation), needle) {
			t.Errorf("reason code %q is missing from docs/reason-codes.md", code)
		}
	}
}
