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

        go = pkgs.go_1_27;
        nodejs = pkgs.nodejs_24;
        pnpm = pkgs.pnpm_10;

        frontendDeps = pnpm.fetchDeps {
          pname = "opsagent-frontend";
          version = "0.0.1";
          src = ./frontend;
          fetcherVersion = 3;
          hash = "sha256-56/Bj6DXnszte1Q7X3gIKkJ1jha+X9HhgThmQj5BaLo=";
        };

        frontend = pkgs.stdenvNoCC.mkDerivation {
          pname = "opsagent-frontend";
          version = "0.0.1";

          src = ./frontend;

          nativeBuildInputs = [ nodejs pnpm pnpm.configHook ];

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

        packages.default = pkgs.buildGo127Module {
          pname = "opsagent";
          version = "0.0.1";

          src = ./.;

          modRoot = "backend";

          vendorHash = "sha256-HnKSFAFeliXwbKw5ZTzelCCbZLJoPFHhjj1fmhQuC6I=";

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
