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
                git-lfs # Large file storage for issue images
              ]);
          };
        };
      }
    );
}
