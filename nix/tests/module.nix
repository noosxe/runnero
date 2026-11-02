# NixOS VM test for the runnero module (docs/39 §7), plus evaluation checks
# for the module's secret guards.
{ self, pkgs }:
let
  inherit (pkgs) lib;

  tag = lib.fileContents ../../VERSION;

  # --- Evaluation checks ----------------------------------------------------
  # Evaluated with the test itself: a regression fails `nix flake check`
  # before any VM boots.
  evalWith =
    runnero:
    import (pkgs.path + "/nixos/lib/eval-config.nix") {
      system = null;
      modules = [
        self.nixosModules.default
        {
          nixpkgs.hostPlatform = pkgs.stdenv.hostPlatform.system;
          system.stateVersion = "26.05";
          boot.loader.grub.enable = false;
          fileSystems."/" = {
            device = "nodev";
            fsType = "ext4";
          };
          virtualisation.docker.enable = true;
          services.runnero = {
            enable = true;
            environmentFile = "/run/secrets/runnero.env";
          }
          // runnero;
        }
      ];
    };

  failedAssertions =
    runnero: map (a: a.message) (lib.filter (a: !a.assertion) (evalWith runnero).config.assertions);

  # A secret key in settings trips the assertion with the documented message.
  secretInSettings = failedAssertions { settings.db-encryption-key = "not-a-real-key"; };

  # A store path is rejected by the environmentFile type.
  storePathEnvFile = builtins.tryEval (
    builtins.deepSeq (evalWith {
      environmentFile = pkgs.writeText "runnero.env" "";
    }).config.services.runnero.environmentFile null
  );

  evalChecks =
    lib.assertMsg (failedAssertions { } == [ ]) "runnero: the baseline configuration fails its assertions"
    && lib.assertMsg (
      lib.length secretInSettings == 1
      && lib.hasInfix "must not contain secrets" (lib.head secretInSettings)
      && lib.hasInfix "db-encryption-key" (lib.head secretInSettings)
    ) "runnero: a secret key in settings did not fail evaluation as documented"
    && lib.assertMsg (!storePathEnvFile.success) "runnero: a store-path environmentFile was accepted";

  # --- VM test --------------------------------------------------------------
  # tsnet resolves the control URL through DNS, not /etc/hosts, so the
  # control server is addressed by its VM IP (first node on vlan 1).
  headscaleIP = "192.168.1.1";
  controlURL = "https://${headscaleIP}";

  # Self-signed TLS for the headscale control server the supervisor's
  # embedded tsnet node enrolls against (exercises the tsnet boot path under
  # the unit's hardening without reaching the internet).
  tlsCert = pkgs.runCommand "headscale-cert" { nativeBuildInputs = [ pkgs.openssl ]; } ''
    openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
      -out cert.pem -keyout key.pem \
      -subj '/CN=headscale' -addext "subjectAltName=IP:${headscaleIP}"
    mkdir -p $out
    cp key.pem cert.pem $out
  '';
in
assert evalChecks;
pkgs.testers.runNixOSTest {
  name = "runnero-nixos-module";

  nodes = {
    headscale = {
      services.headscale = {
        enable = true;
        port = 8080;
        settings = {
          server_url = controlURL;
          ip_prefixes = [ "100.64.0.0/10" ];
          derp = {
            server = {
              enabled = true;
              region_id = 999;
              stun_listen_addr = "0.0.0.0:3478";
            };
            urls = [ ];
          };
          dns = {
            base_domain = "tailnet";
            override_local_dns = false;
          };
        };
      };
      services.nginx = {
        enable = true;
        virtualHosts.${headscaleIP} = {
          addSSL = true;
          sslCertificate = "${tlsCert}/cert.pem";
          sslCertificateKey = "${tlsCert}/key.pem";
          locations."/" = {
            proxyPass = "http://127.0.0.1:8080";
            proxyWebsockets = true;
          };
        };
      };
      networking.firewall.allowedTCPPorts = [
        80
        443
      ];
      networking.firewall.allowedUDPPorts = [ 3478 ];
      environment.systemPackages = [ pkgs.headscale ];
    };

    machine = {
      imports = [ self.nixosModules.default ];

      virtualisation.memorySize = 2048;
      virtualisation.docker.enable = true;
      security.pki.certificateFiles = [ "${tlsCert}/cert.pem" ];
      environment.systemPackages = [ pkgs.curl ];

      services.runnero = {
        enable = true;
        openFirewall = true;
        # Written by the test script with a key generated at test time.
        environmentFile = "/run/runnero.env";
        settings = {
          backup-retention-count = 3;
          tailscale-hostname = "runnero";
          tailscale-funnel = "false";
        };
      };
    };
  };

  testScript =
    { nodes, ... }:
    assert nodes.headscale.networking.primaryIPAddress == headscaleIP;
    ''
    import secrets

    start_all()

    headscale.wait_for_unit("headscale")
    headscale.wait_for_open_port(443)
    headscale.succeed("headscale users create test")
    authkey = headscale.succeed("headscale preauthkeys -u 1 create --reusable").strip()

    # Generated at test time, never committed (docs/39 §7).
    key = secrets.token_urlsafe(32)

    machine.wait_for_unit("docker.service")
    # Unprivileged tsnet cannot set SO_MARK, so it binds control dials to the
    # default-route interface; route the default via the test vlan instead of
    # QEMU's user-mode eth0 so the control server is reachable.
    machine.succeed(
        "while ip route del default 2>/dev/null; do :; done; "
        "ip route add default via ${headscaleIP} dev eth1"
    )
    def write_env(*lines):
        body = " ".join(f"'{line}'" for line in lines)
        machine.succeed(f"umask 077 && printf '%s\\n' {body} > /run/runnero.env")

    def restart_runnero():
        # The unit may have hit its start limit while the file was missing
        # or while the daemon was failing.
        machine.succeed("systemctl reset-failed runnero.service || true")
        machine.succeed("systemctl restart runnero.service")

    with subtest("tsnet boot path under the unit's hardening"):
        write_env(
            f"SUPERVISOR_DB_ENCRYPTION_KEY={key}",
            f"SUPERVISOR_TAILSCALE_AUTHKEY={authkey}",
            "TS_CONTROL_URL=${controlURL}",
        )
        restart_runnero()
        # The embedded node enrolls and reaches Running (state dir, netlink
        # monitor, control dial, userspace WireGuard all pass the sandbox).
        headscale.wait_until_succeeds(
            "journalctl -u headscale.service --no-pager | grep 'node connected.*node.name=runnero'",
            timeout=120,
        )
        machine.wait_until_succeeds(
            "journalctl -u runnero.service --no-pager | grep -F 'Starting -> Running'", timeout=120
        )
        # The daemon then aborts opening the tailnet HTTPS listener: headscale
        # cannot issue ts.net certificates. That is the control server's
        # limit, not the unit's, so the remaining checks run without tsnet.
        machine.wait_until_succeeds(
            "journalctl -u runnero.service --no-pager | grep -F 'you must enable HTTPS'", timeout=60
        )
        machine.succeed("systemctl stop runnero.service")

    write_env(f"SUPERVISOR_DB_ENCRYPTION_KEY={key}")
    restart_runnero()
    machine.wait_for_unit("runnero.service")

    with subtest("readiness: database, pool loop, Docker socket"):
        machine.wait_until_succeeds("curl -fsS http://127.0.0.1:8090/readyz", timeout=120)

    with subtest("process identity and Docker access scope"):
        pid = machine.succeed("systemctl show -P MainPID runnero.service").strip()
        assert pid != "0", "runnero.service has no main PID"
        user = machine.succeed(f"stat -c %U /proc/{pid}").strip()
        assert user == "runnero", f"daemon runs as {user!r}, expected runnero"
        groups = machine.succeed("id -nG runnero").split()
        assert "docker" not in groups, f"runnero user is in the docker group: {groups}"
        docker_gid = machine.succeed("getent group docker | cut -d: -f3").strip()
        machine.succeed(f"grep -E '^Groups:.*\\b{docker_gid}\\b' /proc/{pid}/status")

    with subtest("state directory"):
        perms = machine.succeed("stat -c '%a %U:%G' /var/lib/runnero").strip()
        assert perms == "750 runnero:runnero", f"/var/lib/runnero is {perms!r}"
        machine.succeed("test -f /var/lib/runnero/supervisor.db")

    with subtest("runnero-admin"):
        out = machine.succeed("runnero-admin version")
        assert "runnero-supervisor version ${tag}" in out, f"unexpected version output: {out!r}"
        out = machine.succeed("runnero-admin backup")
        assert "Successfully created backup snapshot" in out, f"unexpected backup output: {out!r}"
        machine.succeed("ls /var/lib/runnero/backups/supervisor-*.db")
        machine.fail("su -s /bin/sh runnero -c 'runnero-admin version'")

    with subtest("no store leak of the secrets"):
        for secret in (key, authkey):
            hits = machine.succeed(
                "nix-store -qR /run/current-system | xargs grep -rlsF -- "
                f"'{secret}' || true"
            ).strip()
            assert hits == "", f"secret found in the store: {hits}"

    with subtest("clean stop"):
        machine.succeed("systemctl stop runnero.service")
        result = machine.succeed("systemctl show -P Result runnero.service").strip()
        assert result == "success", f"runnero.service stopped with result {result!r}"
  '';
}
