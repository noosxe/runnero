package server

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with module="server"
// (docs/06 §1: per-module loggers are mandated for every backend module).
// The Echo/ConnectRPC web server logs through it from M7 on.
var logger = logging.For("server")
