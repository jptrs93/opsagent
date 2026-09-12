{
  description = "OpenDeploy hostile build probe: records what a derivation builder can reach";

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
          # The probe runs inside the derivation builder, which is the position a
          # compromised dependency or build hook would hold: an unprivileged
          # nixbld user in the build container with the sandbox off. Every line
          # it writes is asserted by the e2e suite from the running image.
          probe = pkgs.runCommand "hostile-probe" { nativeBuildInputs = [ pkgs.curl pkgs.coreutils pkgs.gawk ]; } ''
            mkdir -p $out
            report() { echo "hostilebuild probe=$1 result=$2''${3:+ detail=$3}"; }
            exists() { if [ -e "$1" ]; then echo present; else echo absent; fi; }
            http() { curl -ks --max-time 8 -o /dev/null -w '%{http_code}' "$1" 2>/dev/null || true; }
            reach() { code=$(http "$1"); if [ -n "$code" ] && [ "$code" != "000" ]; then echo reachable; else echo unreachable; fi; }
            {
              uid=$(id -u)
              if [ "$uid" -ge 30001 ] && [ "$uid" -le 30032 ]; then report builder_uid nixbld "$uid"; else report builder_uid other "$uid"; fi
              report cap_eff "$(awk '/^CapEff/ {print $2}' /proc/self/status)"
              report agent_data "$(exists /var/lib/opendeploy)"
              report agent_env "$(exists /etc/opendeploy)"
              report containerd_socket "$(exists /run/opendeploy/containerd.sock)"
              report machine_key "$(exists /var/lib/opendeploy/machine.key)"
              bashdir=$(dirname "$(readlink -f "$(command -v bash)")")
              if touch "$bashdir/hostile" 2>/dev/null; then report store_write writable "$bashdir"; else report store_write readonly "$bashdir"; fi
              dns=$(awk '/^nameserver/ {print $2; exit}' /etc/resolv.conf)
              report dns_server "$([ -n "$dns" ] && echo present || echo absent)" "$dns"
              case "$dns" in
                *:*) report cluster_tcp "$(reach "http://[$dns]:80/")" "$dns" ;;
                "") report cluster_tcp skipped ;;
                *) report cluster_tcp "$(reach "http://$dns:80/")" "$dns" ;;
              esac
              report metadata "$(reach http://169.254.169.254/latest/meta-data/)"
              report host_loopback "$(reach http://127.0.0.1:9443/)"
              report public_https "$(reach https://cache.nixos.org/nix-cache-info)"
            } > $out/report.txt
            cat $out/report.txt
          '';
          # membomb touches memory until the build container's cgroup limit kills
          # it, which the preparer must report as a memory limit failure.
          bomb = pkgs.runCommand "hostile-membomb" { nativeBuildInputs = [ pkgs.python3 ]; } ''
            python3 -c '
            chunks = []
            for _ in range(512):
                chunks.append(b"x" * (128 * 1024 * 1024))
            '
            mkdir -p $out
          '';
        in
        {
          default = n2c.buildImage {
            name = "opendeploy-test/hostilebuild";
            config = {
              entrypoint = [ "${pkgs.bash}/bin/bash" "-c" "${pkgs.coreutils}/bin/cat ${probe}/report.txt; exec ${pkgs.coreutils}/bin/sleep infinity" ];
            };
            maxLayers = 16;
          };
          notimage = probe;
          membomb = n2c.buildImage {
            name = "opendeploy-test/hostilebuild-membomb";
            config = {
              entrypoint = [ "${pkgs.bash}/bin/bash" "-c" "exec ${pkgs.coreutils}/bin/sleep infinity # ${bomb}" ];
            };
            maxLayers = 16;
          };
        });
    };
}
