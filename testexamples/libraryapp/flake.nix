{
  description = "OpenDeploy combined library app example";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f (import nixpkgs { inherit system; }));
    in
    {
      packages = forAllSystems (pkgs:
        let
          nodejs = pkgs.nodejs_24;
          pnpm = pkgs.pnpm_10;

          frontendDeps = pkgs.fetchPnpmDeps {
            pname = "opendeploy-library-example-frontend";
            version = "1.0.0";
            src = ./frontend;
            fetcherVersion = 3;
            hash = pkgs.lib.fakeHash;
          };

          frontend = pkgs.stdenvNoCC.mkDerivation {
            pname = "opendeploy-library-example-frontend";
            version = "1.0.0";
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

          app = pkgs.buildGoModule {
            pname = "opendeploy-library-example";
            version = "1.0.0";
            src = ./.;
            modRoot = "backend";
            vendorHash = pkgs.lib.fakeHash;
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
          default = pkgs.dockerTools.streamLayeredImage {
            name = "opendeploy-test/library-app";
            tag = "latest";
            contents = [ pkgs.cacert ];
            config = {
              Cmd = [ "${app}/bin/library-app" ];
              WorkingDir = "/";
              Env = [ "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt" ];
              ExposedPorts = { "8080/tcp" = { }; };
            };
          };
        });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go_1_25 pkgs.nodejs_24 pkgs.pnpm_10 ];
        };
      });
    };
}
