package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/spf13/cobra"
)

func cliInputError(err error) error {
	return &app.Error{Kind: app.ErrorInput, Err: err}
}

func minimumArgs(count int) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if err := cobra.MinimumNArgs(count)(command, args); err != nil {
			return cliInputError(err)
		}
		return nil
	}
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(count)(command, args); err != nil {
			return cliInputError(err)
		}
		return nil
	}
}

func expandInputPaths(values []string) ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	for _, value := range values {
		if !strings.ContainsAny(value, "*?[") {
			path := filepath.Clean(value)
			if !seen[path] {
				seen[path] = true
				result = append(result, path)
			}
			continue
		}
		matches, err := filepath.Glob(value)
		if err != nil {
			return nil, &app.Error{Kind: app.ErrorInput, Err: fmt.Errorf("invalid input glob %q: %w", value, err)}
		}
		if len(matches) == 0 {
			return nil, &app.Error{Kind: app.ErrorInput, Err: fmt.Errorf("input glob %q did not match any files", value)}
		}
		for _, match := range matches {
			path := filepath.Clean(match)
			if !seen[path] {
				seen[path] = true
				result = append(result, path)
			}
		}
	}
	return result, nil
}
