package cron

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with module="cron"
// (docs/06 §1: per-module loggers are mandated for every backend module).
var logger = logging.For("cron")
