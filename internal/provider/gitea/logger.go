package gitea

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with
// module="provider.gitea" (docs/06 §1: per-module loggers are mandated
// for every backend module), the Gitea GitProvider implementation.
var logger = logging.For("provider.gitea")
