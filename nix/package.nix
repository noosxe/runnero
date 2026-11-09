{
  lib,
  stdenvNoCC,
  buildGo127Module,
  fetchPnpmDeps,
  pnpmConfigHook,
  pnpmBuildHook,
  pnpm_11,
  nodejs_24,
}:

# Supervisor package (docs/39 §3.2): two derivations mirroring the two build
# stages of Dockerfile.supervisor — the Vite/React frontend, then the CGO-free
# Go binary embedding it.
let
  fs = lib.fileset;

  # Release tag verbatim (e.g. "v1.0.0"); see docs/39 §3.6. The release
  # workflow refuses to release a tag that differs from this file.
  tag = lib.fileContents ../VERSION;
  version = lib.removePrefix "v" tag;

  frontend = stdenvNoCC.mkDerivation (finalAttrs: {
    pname = "runnero-web";
    inherit version;

    src = fs.toSource {
      root = ../.;
      fileset = fs.difference ../web ../web/embed.go;
    };

    pnpmRoot = "web";

    pnpmDeps = fetchPnpmDeps {
      inherit (finalAttrs) pname version src;
      sourceRoot = "${finalAttrs.src.name}/web";
      pnpm = pnpm_11;
      fetcherVersion = 5;
      hash = "sha256-RNXDypWL1cYU9MHng8GL9D5gRTqU9cj8RIGGy+26S58=";
    };

    nativeBuildInputs = [
      nodejs_24
      pnpm_11
      pnpmConfigHook
      pnpmBuildHook
    ];

    installPhase = ''
      runHook preInstall
      cp -r web/dist $out
      runHook postInstall
    '';
  });
in
buildGo127Module {
  pname = "runnero-supervisor";
  inherit version;

  src = fs.toSource {
    root = ../.;
    fileset = fs.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
      ../proto
      ../web/embed.go
    ];
  };

  vendorHash = "sha256-n9vhrKxHiBUNYqEOQ3lfA4H3FWpWoVeYISwZv4dXBX0=";

  subPackages = [ "cmd/runnero-supervisor" ];

  env.CGO_ENABLED = 0;

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${tag}"
  ];

  # go:embed in web/embed.go needs the built SPA at compile time.
  preBuild = ''
    cp -r ${frontend} web/dist
  '';

  # The Go suite runs in go.yml; the NixOS VM test covers the package as
  # deployed (docs/39 §7).
  doCheck = false;

  passthru = { inherit frontend; };

  meta = {
    description = "Self-hosted, multi-provider Actions runner supervisor";
    homepage = "https://github.com/noosxe/runnero";
    mainProgram = "runnero-supervisor";
    platforms = lib.platforms.linux;
  };
}
