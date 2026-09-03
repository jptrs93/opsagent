{
  description = "OpenDeploy metrics load generator test image";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f (import nixpkgs { inherit system; }));
    in
    {
      packages = forAllSystems (pkgs:
        let
          app = pkgs.buildGoModule {
            pname = "loadgen";
            version = "0.1.0";
            src = ./.;
            vendorHash = null;
          };
        in
        {
          default = pkgs.dockerTools.streamLayeredImage {
            name = "opendeploy-test/loadgen";
            tag = "latest";
            config = {
              Cmd = [ "${app}/bin/loadgen" ];
              WorkingDir = "/";
            };
          };
          app = app;
        });
    };
}
