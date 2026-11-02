{
  description = "runnero: self-hosted Actions runner supervisor";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = import ./nix/devshell.nix { inherit pkgs; };
      }
    )
    # The supervisor is Linux-only (docs/39 §3.1).
    // flake-utils.lib.eachSystem [ "x86_64-linux" "aarch64-linux" ] (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages = rec {
          runnero-supervisor = pkgs.callPackage ./nix/package.nix { };
          default = runnero-supervisor;
        };
      }
    )
    // {
      nixosModules = rec {
        runnero = import ./nix/module.nix self;
        default = runnero;
      };

      checks.x86_64-linux.nixos-module = import ./nix/tests/module.nix {
        inherit self;
        pkgs = nixpkgs.legacyPackages.x86_64-linux;
      };
    };
}
