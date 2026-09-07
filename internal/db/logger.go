package db

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with module="db"
// (docs/06 §1: per-module loggers are mandated for every backend module).
// The persistence layer lands in M2 (RUN-12..RUN-18) and logs through it.
var logger = logging.For("db")
