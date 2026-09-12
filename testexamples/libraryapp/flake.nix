{
  description = "OpenDeploy combined library app example";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  inputs.nix2container.url = "github:nlewo/nix2container";
  inputs.nix2container.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { nixpkgs, nix2container, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system (import nixpkgs { inherit system; }));
    in
    {
      packages = forAllSystems (system: pkgs:
        let
          nodejs = pkgs.nodejs_24;
          pnpm = pkgs.pnpm_11;
          frontendSrc = pkgs.lib.cleanSourceWith {
            src = ./frontend;
            filter = path: _type:
              let name = builtins.baseNameOf path;
              in name != "node_modules" && name != ".vite";
          };
          appSrc = pkgs.lib.cleanSourceWith {
            src = ./.;
            filter = path: _type:
              let name = builtins.baseNameOf path;
              in name != "node_modules" && name != ".vite" && name != "dist";
          };

          frontendDeps = pkgs.fetchPnpmDeps {
            pname = "opendeploy-library-example-frontend";
            version = "1.0.0";
            src = frontendSrc;
            fetcherVersion = 4;
            hash = "sha256-KA3uzCciHgLWTZUOuFxRD2GUIeQlppSFvsfPi8y03NU=";
          };

          frontend = pkgs.stdenvNoCC.mkDerivation {
            pname = "opendeploy-library-example-frontend";
            version = "1.0.0";
            src = frontendSrc;
            nativeBuildInputs = [ nodejs pnpm pkgs.pnpmConfigHook ];
            pnpmDeps = frontendDeps;
            buildPhase = ''
              runHook preBuild
              pnpm run build --outDir $out
              runHook postBuild
            '';
            dontInstall = true;
          };

          app = pkgs.buildGoModule {
            pname = "opendeploy-library-example";
            version = "1.0.0";
            src = appSrc;
            modRoot = "backend";
            vendorHash = null;
            subPackages = [ "." ];
            preBuild = ''
              mkdir -p web/dist
              cp -r ${frontend}/* web/dist/
            '';
            postInstall = ''
              mv $out/bin/backend $out/bin/library-app
            '';
            doCheck = false;
            ldflags = [ "-s" "-w" ];
          };
        in
        {
          frontend = frontend;
          app = app;
          default = if pkgs.stdenv.isLinux then
            nix2container.packages.${system}.nix2container.buildImage {
              name = "opendeploy-test/library-app";
              copyToRoot = [ pkgs.cacert ];
              config = {
                entrypoint = [ "${app}/bin/library-app" ];
                workingdir = "/";
                env = [ "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt" ];
                exposedports = { "8080/tcp" = { }; };
              };
              maxLayers = 16;
            }
          else
            app;
        });

      devShells = forAllSystems (system: pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go_1_25 pkgs.nodejs_24 pkgs.pnpm_11 ];
        };
      });
    };
}
