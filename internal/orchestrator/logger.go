package orchestrator

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with module="orchestrator"
// (docs/06 §1: per-module loggers are mandated for every backend module).
// The container lifecycle engine logs through it from M3 on.
var logger = logging.For("orchestrator")
