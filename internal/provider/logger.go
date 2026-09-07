package provider

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with module="provider"
// (docs/06 §1: per-module loggers are mandated for every backend module).
// The GitProvider engine logs through it from M3 on.
var logger = logging.For("provider")
