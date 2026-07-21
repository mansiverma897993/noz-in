package main

import (
	"fmt"

	"github.com/mansiverma897993/signoz/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(command *cobra.Command, _ []string) error {
			if jsonOutput(command.Flags()) {
				return writeJSONLine(outputWriter, map[string]string{"name": "promcast", "version": version.Version(), "commit": version.Commit()})
			}
			_, err := fmt.Fprintf(outputWriter, "promcast %s (%s)\n", version.Version(), version.Commit())
			return err
		},
	}
}
