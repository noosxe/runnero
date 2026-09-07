package forgejo

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with
// module="provider.forgejo" (docs/06 §1: per-module loggers are mandated
// for every backend module), the Forgejo GitProvider implementation.
var logger = logging.For("provider.forgejo")
