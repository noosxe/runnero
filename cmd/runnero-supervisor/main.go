// Command supervisor is the AIO Supervisor daemon entrypoint.
//
// It manages dynamic pools of ephemeral GitHub/Gitea/Forgejo runner
// containers and serves the embedded web control interface.
package main

import "os"

func main() {
	if err := Execute(); err != nil {
		os.Exit(1)
	}
}
