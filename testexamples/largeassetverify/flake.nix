{
  description = "OpenDeploy large asset verifier E2E image";

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
            pname = "largeassetverify";
            version = "0.1.0";
            src = ./.;
            vendorHash = null;
          };
        in
        {
          default = pkgs.dockerTools.streamLayeredImage {
            name = "opendeploy-test/largeassetverify";
            tag = "latest";
            config = {
              Cmd = [ "${app}/bin/largeassetverify" ];
              WorkingDir = "/";
            };
          };
          app = app;
        });
    };
}
