package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newResetPasswordCommand creates the `supervisor reset-password`
// subcommand: the local administrator's password recovery path (open
// questions #8). It is meant to be run inside the supervisor container
// via docker exec.
func newResetPasswordCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-password",
		Short: "Interactively reset an administrator password",
		Long: `Prompts for the administrator username and a new password (with
confirmation), then updates the credential hash in the database. Run it
inside the supervisor container, e.g.

  docker exec -it <supervisor-container> supervisor reset-password`,
		RunE: runResetPassword,
	}
}

// runResetPassword is a stub. Later milestones add the interactive
// username and password prompts (hidden input, with confirmation).
func runResetPassword(cmd *cobra.Command, args []string) error {
	logger.Info("reset-password: prompting not implemented yet")
	return fmt.Errorf("reset-password: %w", errNotImplemented)
}
