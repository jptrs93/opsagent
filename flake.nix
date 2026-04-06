{
  description = "Opsagent – deployment management tool";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        go = pkgs.go_1_25;
        nodejs = pkgs.nodejs_22;
        pnpm = pkgs.pnpm_10;

        frontendDeps = pkgs.fetchPnpmDeps {
          pname = "opsagent-frontend";
          version = "0.0.1";
          src = ./frontend;
          fetcherVersion = 2;
          hash = "sha256-wwbrvDjqkWHc5TFpPlIvAFoEAgIPicyEg3u3ESQ/D/8=";
        };

        frontend = pkgs.stdenvNoCC.mkDerivation {
          pname = "opsagent-frontend";
          version = "0.0.1";

          src = ./frontend;

          nativeBuildInputs = [ nodejs pnpm pkgs.pnpmConfigHook ];

          pnpmDeps = frontendDeps;

          buildPhase = ''
            runHook preBuild
            pnpm run build --outDir $out
            runHook postBuild
          '';

          dontInstall = true;
        };
      in
      {
        packages.frontend = frontend;

        packages.default = pkgs.buildGoModule {
          pname = "opsagent";
          version = "0.0.1";

          src = ./.;

          modRoot = "backend";

          vendorHash = "sha256-JtTRswvxwbSXe9VMvFW1O6E4UDoiUiiYEs7DXRSMT0s=";

          subPackages = [ "." ];

          preBuild = ''
            mkdir -p web/dist
            cp -r ${frontend}/* web/dist/
          '';

          doCheck = false;

          ldflags = [ "-s" "-w" ];

          meta = with pkgs.lib; {
            description = "Opsagent – deployment management tool";
            mainProgram = "backend";
          };
        };

        devShells.default = pkgs.mkShell {
          buildInputs = [ go nodejs pnpm ];
        };
      }
    );
}
