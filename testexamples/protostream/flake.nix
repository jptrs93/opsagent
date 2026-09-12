{
  description = "OpenDeploy cleanproto streaming test image";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  inputs.nix2container.url = "github:nlewo/nix2container";
  inputs.nix2container.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { nixpkgs, nix2container, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system (import nixpkgs { inherit system; }));
    in
    {
      packages = forAllSystems (system: pkgs:
        let
          n2c = nix2container.packages.${system}.nix2container;
          app = pkgs.buildGoModule {
            pname = "protostream";
            version = "0.1.0";
            src = ./.;
            vendorHash = null;
          };
        in
        {
          default = n2c.buildImage {
            name = "opendeploy-test/protostream";
            config = {
              entrypoint = [ "${app}/bin/protostream" ];
            };
            maxLayers = 16;
          };
          app = app;
        });
    };
}
