package main

import (
	"fmt"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/report"
	"github.com/spf13/cobra"
)

func newReportCommand() *cobra.Command {
	var outputPath string
	command := &cobra.Command{
		Use:   "report <report.json>...",
		Short: "Render evidence JSON as a self-contained HTML report",
		Args:  minimumArgs(1),
		RunE: func(command *cobra.Command, paths []string) error {
			if outputPath != "" && len(paths) != 1 {
				return cliInputError(fmt.Errorf("--out can only be used with one report"))
			}
			for _, path := range paths {
				destination := outputPath
				if destination == "" {
					destination = report.DefaultHTMLPath(path)
				}
				if err := report.RenderFile(path, destination); err != nil {
					return &app.Error{Kind: app.ErrorInput, Err: err}
				}
				if jsonOutput(command.Flags()) {
					if err := writeJSONLine(outputWriter, map[string]string{"type": "report", "input": path, "html": destination}); err != nil {
						return err
					}
				} else if _, err := fmt.Fprintln(outputWriter, terminalSafe(destination)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVarP(&outputPath, "out", "o", "", "output HTML path (one input only)")
	return command
}
