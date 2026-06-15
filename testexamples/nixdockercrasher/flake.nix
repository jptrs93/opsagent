{
  description = "OpenDeploy nixDockerBuild crasher test image";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f (import nixpkgs { inherit system; }));
    in
    {
      packages = forAllSystems (pkgs:
        let
          app = pkgs.buildGoModule {
            pname = "nixdockercrasher";
            version = "0.1.0";
            src = ./.;
            vendorHash = null;
          };
        in
        {
          default = pkgs.dockerTools.streamLayeredImage {
            name = "opendeploy-test/nixdockercrasher";
            tag = "latest";
            config = {
              Cmd = [ "${app}/bin/nixdockercrasher" ];
              WorkingDir = "/";
            };
          };
          app = app;
        });
    };
}
