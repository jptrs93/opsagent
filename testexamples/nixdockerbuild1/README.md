# nixdockerbuild1

Tiny `nixDockerBuild` test app. The flake default output is an executable image
stream from `pkgs.dockerTools.streamLayeredImage`, suitable for OpenDeploy's
`prepare.nixDockerBuild` importer.

The container runs a Go program that prints selected environment variables at
startup, then prints an incrementing count every 10 seconds forever.

Example deployment request shape:

```json
{
  "identity": {
    "name": "nixdockerbuild1",
    "spaceId": 1
  },
  "nodeId": 1,
  "spec": {
    "prepare": {
      "nixDockerBuild": {
        "repo": "github.com/jptrs93/opsagent",
        "flake": "testexamples/nixdockerbuild1/flake.nix"
      }
    },
    "runner": {
      "container": {
        "disableDataVolume": true
      }
    }
  }
}
```
