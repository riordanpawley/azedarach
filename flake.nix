{
  inputs = {
    nixpkgs = {
      url = "github:nixos/nixpkgs/nixpkgs-unstable";
    };

    flake-utils = {
      url = "github:numtide/flake-utils";
    };

    # Emergent Learning Framework - persistent memory for Claude Code
    elf = {
      url = "github:Spacehunterz/Emergent-Learning-Framework_ELF";
      flake = false;
    };

  };

  outputs =
    {
      nixpkgs,
      flake-utils,
      ...
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        corepackEnable = pkgs.runCommand "corepack-enable" { } ''
          mkdir -p $out/bin
          ${pkgs.nodejs_22}/bin/corepack enable --install-directory $out/bin
        '';
        brRelease = {
          version = "0.1.20";
          assets = {
            aarch64-darwin = {
              file = "br-v0.1.20-darwin_arm64.tar.gz";
              hash = "sha256-XsHX710UcZxJ1zbBCChK2gHOy0BISpQT7OGgdJzvRC8=";
            };
            x86_64-darwin = {
              file = "br-v0.1.20-darwin_amd64.tar.gz";
              hash = "sha256-wB0NdFkETTRCby7PN8fktfbvOd7fRNQ/MRbkN/3eNF0=";
            };
            x86_64-linux = {
              file = "br-v0.1.20-linux_amd64.tar.gz";
              hash = "sha256-mU5WDf0gFKTvVLXwo4A7jMVWnkxZ7QWHbeBNH385r1M=";
            };
            aarch64-linux = {
              file = "br-v0.1.20-linux_arm64.tar.gz";
              hash = "sha256-FLH4hXpRaZUeyvX3+Kf0avBfvE5CooDoZ/8ImLC+rww=";
            };
          };
        };
        brAsset =
          brRelease.assets.${system}
            or (throw "Unsupported system for beads_rust br: ${system}");
        br = pkgs.runCommandNoCC "br-${brRelease.version}" {
          src = pkgs.fetchzip {
            url = "https://github.com/Dicklesworthstone/beads_rust/releases/download/v${brRelease.version}/${brAsset.file}";
            hash = brAsset.hash;
            stripRoot = false;
          };
        } ''
          mkdir -p "$out/bin"

          br_bin="$(find "$src" -type f -name br | head -n1)"
          if [ -z "$br_bin" ]; then
            echo "Could not find br binary in release archive" >&2
            exit 1
          fi

          install -m755 "$br_bin" "$out/bin/br"
        '';
      in
      {
        formatter = pkgs.alejandra;

        devShells = {
          default = pkgs.mkShell {
            buildInputs =
              (with pkgs; [
                gh
                bun
                nodejs_22
                corepackEnable
                vtsls
                biome
                viu # Terminal image viewer with Kitty graphics protocol support
                go
                git-lfs # Large file storage for bead images
              ])
              ++ [ br ];
          };
        };
      }
    );
}
