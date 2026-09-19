package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCommand builds the `supervisor version` subcommand (RUN-250).
// Cobra derives the root's --version flag from the same main.version
// variable, but that flag silently disappears when the build-time ldflags
// stamp is empty (the RUN-249 failure mode, where an unstamped binary
// reported no version at all). The subcommand is the explicit,
// always-present surface for operators and scripts to interrogate a
// deployed binary. The output format mirrors cobra's --version template.
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use: "version",
		// Cobra runs the nearest PersistentPreRunE up the command chain;
		// overriding the root's here (which loads and validates the full
		// config) keeps `supervisor version` usable on any box, including
		// ones with missing or invalid configuration.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		Short:             "Print the supervisor version and exit",
		Args:              cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "runnero-supervisor version %s\n", version)
			return err
		},
	}
}
