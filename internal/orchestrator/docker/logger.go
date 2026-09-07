package docker

import "github.com/noosxe/runnero/internal/logging"

// logger tags every record emitted by this package with
// module="orchestrator.docker" (docs/06 §1: per-module loggers are
// mandated for every backend module), the Docker-backed ContainerProvider
// implementation of docs/02 §3.1.
var logger = logging.For("orchestrator.docker")
