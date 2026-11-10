{
  description = "Development environment for zpgo";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
    zpg = {
      url = "github:asciimoth/zpg";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.git-hooks.follows = "git-hooks";
    };
    # Keep the minimum supported Go version available for compatibility checks.
    nixpkgs-go122.url = "github:NixOS/nixpkgs/nixos-24.11";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-go122,
      git-hooks,
      zpg,
      ...
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      eachSystem = nixpkgs.lib.genAttrs systems;
      goModulesFor =
        system:
        (nixpkgs.legacyPackages.${system}.buildGoModule {
          pname = "zpgo";
          version = "dev";
          src = nixpkgs.lib.cleanSource ./.;
          proxyVendor = true;
          vendorHash = "sha256-C4gw9pPive9ItzLhRCsnpFGdqD4lx0pwi0Dc/G0OHBg=";
        }).goModules;
    in
    {
      checks = eachSystem (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          checkGo =
            go:
            pkgs.runCommand "zpgo-go-${go.version}-check"
              {
                nativeBuildInputs = [
                  go
                  pkgs.stdenv.cc
                  zpg.packages.${system}.zpg
                ];
                src = pkgs.lib.cleanSource ./.;
                GOTOOLCHAIN = "local";
                GOPROXY = "file://${goModulesFor system}";
                GOSUMDB = "off";
                GOFLAGS = "-mod=readonly";
                ZPGO_REQUIRE_ZPG = "1";
              }
              ''
                export HOME="$TMPDIR"
                export GOCACHE="$TMPDIR/go-cache"
                cp -r "$src" source
                chmod -R u+w source
                cd source
                go build ./...
                go vet ./...
                go test -race -count=1 ./...
                touch "$out"
              '';
        in
        {
          go = checkGo pkgs.go;
          go122 = checkGo nixpkgs-go122.legacyPackages.${system}.go_1_22;
          pre-commit-check = git-hooks.lib.${system}.run {
            src = ./.;
            default_stages = [ "pre-commit" ];
            hooks = {
              check-added-large-files.enable = true;
              check-case-conflicts.enable = true;
              check-merge-conflicts.enable = true;
              check-toml.enable = true;
              check-yaml.enable = true;
              commitizen.enable = true;
              deadnix.enable = true;
              end-of-file-fixer.enable = true;
              gofmt.enable = true;
              markdownlint.enable = true;
              nixfmt.enable = true;
              statix.enable = true;
              trim-trailing-whitespace.enable = true;
              typos.enable = true;
              just-fmt = {
                enable = true;
                name = "Justfile format";
                entry = "${pkgs.just}/bin/just --fmt --check";
                files = "^justfile$";
                pass_filenames = false;
              };
              go-vet = {
                enable = true;
                name = "Go vet";
                entry = "${pkgs.coreutils}/bin/env GOPROXY=file://${goModulesFor system} GOSUMDB=off ${pkgs.go}/bin/go vet ./...";
                files = "(\\.go$|^go\\.(mod|sum)$)";
                pass_filenames = false;
                require_serial = true;
              };
              go-test = {
                enable = true;
                name = "Go tests";
                entry = "${pkgs.coreutils}/bin/env GOPROXY=file://${goModulesFor system} GOSUMDB=off ${pkgs.go}/bin/go test ./...";
                files = "(\\.go$|^go\\.(mod|sum)$)";
                pass_filenames = false;
                require_serial = true;
              };
            };
          };
        }
      );

      formatter = eachSystem (system: nixpkgs.legacyPackages.${system}.nixfmt);

      devShells = eachSystem (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          hook = self.checks.${system}.pre-commit-check;
          shellFor =
            go:
            pkgs.mkShell {
              packages = [
                go
                pkgs.just
                zpg.packages.${system}.zpg
              ]
              ++ builtins.filter (p: p != pkgs.go) hook.enabledPackages;
              GOTOOLCHAIN = "local";
              GOPROXY = "file://${goModulesFor system}";
              GOSUMDB = "off";
              inherit (hook) shellHook;
            };
        in
        {
          default = shellFor pkgs.go;
          go122 = shellFor nixpkgs-go122.legacyPackages.${system}.go_1_22;
        }
      );
    };
}
