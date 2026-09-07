package github

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with
// module="provider.github" (docs/06 §1: per-module loggers are mandated
// for every backend module), the GitHub GitProvider implementation.
var logger = logging.For("provider.github")
