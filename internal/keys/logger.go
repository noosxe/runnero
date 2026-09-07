package keys

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with module="keys"
// (docs/06 §1: per-module loggers are mandated for every backend module).
// Only non-secret metadata is ever logged; the master key and the derived
// secrets must never appear in a log record.
var logger = logging.For("keys")
