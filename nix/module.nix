# NixOS module: the supervisor as a native systemd service (docs/39).
#
# A function of the flake's `self` so `package` defaults to the flake's own
# build: `nixosModules.default = import ./nix/module.nix self;`.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.runnero;
  settingsFormat = pkgs.formats.yaml { };

  # Rendered into the world-readable store: never put secrets here (enforced
  # by the assertion below, docs/39 §5.1).
  configFile = settingsFormat.generate "runnero.yaml" cfg.settings;

  stateDir = "/var/lib/runnero";
  localSocket = lib.hasPrefix "unix://" cfg.settings.docker-host;
  privilegedPort = cfg.settings.port < 1024;

  secretKeys = [
    "db-encryption-key"
    "webhook-github-secret"
    "webhook-gitea-secret"
    "webhook-forgejo-secret"
    "tailscale-auth-key"
  ];
  leakedKeys = lib.filter (k: cfg.settings ? ${k}) secretKeys;

  # Runs a CLI subcommand as the service identity, with the same config,
  # data dir and secrets (docs/39 §3.5). PID 1 reads the environment file, so
  # the wrapper never touches the secret itself. The config file goes through
  # SUPERVISOR_CONFIG rather than --config: `import` defines its own local
  # --config flag (the file to import), which shadows the root one.
  adminWrapper = pkgs.writeShellApplication {
    name = "runnero-admin";
    runtimeInputs = [ config.systemd.package ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        echo "runnero-admin: must be run as root (try: sudo runnero-admin $*)" >&2
        exit 1
      fi
      exec systemd-run --pty --wait --collect --quiet \
        --uid=runnero --gid=runnero \
        -p EnvironmentFile=${cfg.environmentFile} \
        -p WorkingDirectory=${stateDir} \
        --setenv=SUPERVISOR_CONFIG=${configFile} \
        ${lib.getExe cfg.package} "$@"
    '';
  };
in
{
  options.services.runnero = {
    enable = lib.mkEnableOption "the runnero Actions runner supervisor";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "runnero.packages.\${system}.default";
      description = "The runnero-supervisor package to run.";
    };

    settings = lib.mkOption {
      default = { };
      description = ''
        Supervisor configuration, rendered to YAML and passed as `--config`.
        Keys are the config-file keys of the daemon (the `envKeys` map in
        `internal/config/config.go`). The rendered file lands in the Nix store:
        secrets belong in {option}`services.runnero.environmentFile`.
      '';
      type = lib.types.submodule {
        freeformType = settingsFormat.type;
        options = {
          port = lib.mkOption {
            type = lib.types.port;
            default = 8090;
            description = "HTTP port for the API and web control interface.";
          };
          log-level = lib.mkOption {
            type = lib.types.enum [
              "debug"
              "info"
              "warn"
              "error"
            ];
            default = "info";
            description = "Log level.";
          };
          docker-host = lib.mkOption {
            type = lib.types.str;
            default = "unix:///run/docker.sock";
            description = ''
              Docker engine endpoint. A `tcp://` value (socket proxy, remote
              engine) skips the local Docker assertion and the `docker` group.
            '';
          };
          shutdown-mode = lib.mkOption {
            type = lib.types.enum [
              "detach"
              "drain"
            ];
            default = "detach";
            description = ''
              What stopping the unit does to runner containers (docs/03 §7).
              `detach` leaves every runner running and registered: the next
              start adopts them, so a restart or a `nixos-rebuild switch`
              never interrupts a job. `drain` deregisters idle runners and
              waits for busy ones (up to the `shutdown_timeout_seconds` app
              setting, default 300s) before force-terminating them: use it
              to take the host out of service.
            '';
          };
          data-dir = lib.mkOption {
            type = lib.types.str;
            default = stateDir;
            readOnly = true;
            description = "Data directory; fixed to the unit's StateDirectory.";
          };
        };
      };
    };

    environmentFile = lib.mkOption {
      type = lib.types.nullOr (
        lib.types.pathWith {
          inStore = false;
          absolute = true;
        }
      );
      default = null;
      example = "/run/secrets/runnero.env";
      description = ''
        Dotenv file loaded into the daemon's environment by systemd (read as
        root). Must carry `SUPERVISOR_DB_ENCRYPTION_KEY` and any other secret
        (webhook secrets, `SUPERVISOR_TAILSCALE_AUTHKEY`). Store paths are
        rejected: a path literal would be copied into the world-readable
        Nix store.
      '';
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open {option}`services.runnero.settings.port` on TCP.";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.environmentFile != null;
        message = ''
          services.runnero.environmentFile must be set: the daemon needs
          SUPERVISOR_DB_ENCRYPTION_KEY (at least 32 bytes) from it.
        '';
      }
      {
        assertion = leakedKeys == [ ];
        message = ''
          services.runnero.settings must not contain secrets (found:
          ${lib.concatStringsSep ", " leakedKeys}): the rendered config is
          world-readable in the Nix store. Put them in
          services.runnero.environmentFile as SUPERVISOR_* variables instead.
        '';
      }
      {
        assertion = !localSocket || config.virtualisation.docker.enable;
        message = ''
          services.runnero talks to a local Docker socket
          (settings.docker-host = ${cfg.settings.docker-host}): enable
          virtualisation.docker.enable, or point docker-host at a tcp:// engine.
        '';
      }
    ];

    users.users.runnero = {
      isSystemUser = true;
      group = "runnero";
      home = stateDir;
      description = "runnero supervisor";
    };
    users.groups.runnero = { };

    environment.systemPackages = [ adminWrapper ];

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ cfg.settings.port ];

    systemd.services.runnero = {
      description = "runnero Actions runner supervisor";
      wantedBy = [ "multi-user.target" ];
      # `wants`, not `requires`: a Docker outage is a handled state (/readyz
      # answers degraded); `requires` would bounce the supervisor on every
      # engine restart.
      wants = [
        "network-online.target"
        "docker.service"
        "docker.socket"
      ];
      after = [
        "network-online.target"
        "docker.service"
        "docker.socket"
      ];

      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} daemon --config ${configFile}";
        EnvironmentFile = cfg.environmentFile;
        User = "runnero";
        Group = "runnero";
        # Scoped to the service: the runnero user itself is not in `docker`.
        SupplementaryGroups = lib.optional localSocket "docker";

        StateDirectory = "runnero";
        StateDirectoryMode = "0750";
        WorkingDirectory = stateDir;
        UMask = "0027";

        Restart = "on-failure";
        RestartSec = "5s";
        # Detach (the default) bounds its own shutdown at 10s
        # (daemonShutdownTimeout); runner containers live in Docker's cgroups,
        # not this unit's, so stopping the unit never kills them. Drain waits
        # for busy runners up to shutdown_timeout_seconds (default 300s) plus
        # the same 10s: raise this if you raise that setting (docs/39 §3.4).
        TimeoutStopSec = lib.mkDefault (if cfg.settings.shutdown-mode == "drain" then "6min" else "30s");

        AmbientCapabilities = lib.optional privilegedPort "CAP_NET_BIND_SERVICE";
        CapabilityBoundingSet = lib.optional privilegedPort "CAP_NET_BIND_SERVICE";

        # Hardening (docs/39 §3.4). It limits what a compromised daemon can do
        # to the host directly; it cannot limit what it can do through the
        # Docker socket.
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectControlGroups = true;
        ProtectClock = true;
        ProtectHostname = true;
        ProtectProc = "invisible";
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = [
          "@system-service"
          "~@privileged"
          "~@resources"
        ];
        # AF_NETLINK: tsnet's network monitor (docs/26).
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_INET"
          "AF_INET6"
          "AF_NETLINK"
        ];
      };
    };
  };
}
