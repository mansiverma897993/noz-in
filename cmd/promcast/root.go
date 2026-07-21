package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var outputWriter io.Writer = os.Stdout

func execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := newRootCommand()
	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		code := commandExitCode(err)
		if code != 2 {
			fmt.Fprintln(os.Stderr, terminalSafe(err.Error()))
		}
		os.Exit(code)
	}
}

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           "promcast",
		Short:         "Migrate observability artifacts to SigNoz",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.PersistentFlags().Bool("json", false, "write newline-delimited JSON to stdout")
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return cliInputError(err) })
	command.AddCommand(newGrafanaCommand())
	command.AddCommand(newDiffCommand())
	command.AddCommand(newVerifyCommand())
	command.AddCommand(newPrometheusRulesCommand())
	command.AddCommand(newReportCommand())
	command.AddCommand(newMCPCommand())
	command.AddCommand(newVersionCommand())
	return command
}
