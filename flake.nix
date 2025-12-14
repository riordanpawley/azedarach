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
      elf,
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

        # Python environment for ELF dashboard
        elfPython = pkgs.python312.withPackages (ps: with ps; [
          fastapi
          uvicorn
          aiofiles
          websockets
        ]);

      in
      {
        formatter = pkgs.alejandra;

        devShells = {
          default = pkgs.mkShell {
            buildInputs = with pkgs; [
              gh
              bun
              nodejs_22
              corepackEnable
              vtsls
              biome

              # ELF dependencies
              elfPython
              sqlite
            ];

            # Make ELF source available
            ELF_PATH = "${elf}";

            shellHook = ''
              # Auto-install ELF if not already installed
              if [ ! -d "$HOME/.claude/emergent-learning" ]; then
                echo "🧠 Installing Emergent Learning Framework (ELF)..."
                # Non-interactive install: skip optional swarm (n) and dashboard (n)
                echo -e "n\nn" | bash "$ELF_PATH/install.sh"
                echo "✓ ELF installed! Start Claude sessions with 'check in'"
                echo "  For full install with dashboard/swarm: bash \$ELF_PATH/install.sh"
              fi
            '';
          };
        };
      }
    );
}
