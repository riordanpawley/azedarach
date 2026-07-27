{
  inputs = {
    nixpkgs = {
      url = "github:nixos/nixpkgs/nixpkgs-unstable";
    };

    flake-utils = {
      url = "github:numtide/flake-utils";
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
      in
      {
        formatter = pkgs.alejandra;

        devShells = {
          default = pkgs.mkShell {
            buildInputs = (
              with pkgs;
              [
                gh
                age
                sops
                coreutils # GNU timeout bounds the full merge/rebase gate process tree.
                viu # Terminal image viewer with Kitty graphics protocol support
                go
                git-lfs # Large file storage for issue images
              ]
            );
          };
        };
      }
    );
}
