# nixdockercrasher

Tiny `nixDockerBuild` test app for container crash/restart behavior. The flake
default output is an executable image stream from
`pkgs.dockerTools.streamLayeredImage`, suitable for OpenDeploy's
`prepare.nixDockerBuild` importer.

The container reads `/data/crashcount.txt` on startup. If the count is less than
3, it waits 2 seconds, writes the next crash number to `/data/crashcount.txt`,
then panics. Once the file contains 3 or more, it stops crashing
and prints a health tick every 10 seconds forever.

Use the default container data volume so `/data/crashcount.txt` persists across
restarts.

Example deployment request shape:

```json
{
  "configId": {
    "name": "nixdockercrasher",
    "machine": "primary"
  },
  "spec": {
    "prepare": {
      "nixDockerBuild": {
        "repo": "github.com/jptrs93/opsagent",
        "flake": "testexamples/nixdockercrasher/flake.nix"
      }
    },
    "runner": {
      "container": {}
    }
  }
}
```
