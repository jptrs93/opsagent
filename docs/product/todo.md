# Todo

## Backlog

- Consider splitting Git discovery and exact source validation into dedicated API contracts; the current implementation keeps the existing flag-based endpoint for compatibility.
- Improve validation on resource create and update operations so the system strictly controls data validity at all times.
- Add a log housekeeper that compresses and backs up workload logs. Show total log space used.
- Define the behavior for auto-upgrading deployments that depend on changed assets, configs, or secrets.
- Decide whether deployment writes should require view access on referenced secrets (a pin by guessed id in an invisible space still works within the own-or-global bound now enforced for secrets, configs, and assets).
- Add "used by" information for spaces, showing how many deployments each space contains and which deployments they are (assets, configs, and secrets already have usage overlays).
- Expand the machines page to show more machine details, including network interfaces, IP addresses, CPU cores, architecture, RAM, and disk space.
- Continue the built-in networking layer. Machine-local virtual networking, worker-to-worker fixed-tunnel routing, node-local DNS, and node-local ingress exist; remaining work includes primary-node remote routing, cross-node DNS, network policy and anti-spoofing, multi-node ingress, and service load balancing. Design in [future-work/networking.md](../future-work/networking.md).
- Major feature: add resource monitoring for workloads.
- Add resource requests and limits to deployment configuration.
- Major feature: allow deployments to be defined without specifying a machine. Add a scheduler that automatically schedules deployments onto machines.
- Review Litestream behavior when an existing remote backup is ahead of the local primary database. Define how OpenDeploy should detect, warn, block, or recover before a stale local database can publish a newer backup snapshot.
- Make primary backup restore version-aware. Today `install primary --restore-backup true` installs the current executable unless `--version` selects a release, then restores the DB; it does not inspect backup-visible state to restore or warn about the OpenDeploy version that produced the backup.
