# Todo

## Backlog

- Consider splitting Git discovery and exact source validation into dedicated API contracts; the current implementation keeps the existing flag-based endpoint for compatibility.
- Improve validation on resource create and update operations so the system strictly controls data validity at all times.
- Optimize internal asset storage. Consider content hashes and compression.
- Add a log housekeeper that compresses and backs up workload logs. Show total log space used.
- Define the behavior for auto-upgrading deployments that depend on changed assets, configs, or secrets.
- Decide whether configs and assets get the same own-or-global reference-locality rule now enforced for secrets, and whether deployment writes should additionally require view access on referenced secrets (a pin by guessed id in an invisible space still works within the own-or-global bound).
- Add "used by" information for assets, configs, secrets, and spaces. Show how many deployments use each item and which deployments use it.
- Expand the machines page to show more machine details, including network interfaces, IP addresses, CPU cores, architecture, RAM, and disk space.
- Continue the built-in networking layer. Machine-local virtual networking and port forwarding exist; remaining work includes fixed IP-in-IP cross-machine routing, network policy, ingress, and service load balancing. Design in [future-work/networking.md](../future-work/networking.md).
- Add strict underlay-address validation and cluster-wide address-family consistency checks before tunnel reconciliation is enabled.
- Major feature: add resource monitoring for workloads.
- Add resource requests and limits to deployment configuration.
- Major feature: allow deployments to be defined without specifying a machine. Add a scheduler that automatically schedules deployments onto machines.
- Review Litestream behavior when an existing remote backup is ahead of the local primary database. Define how OpenDeploy should detect, warn, block, or recover before a stale local database can publish a newer backup snapshot.
- Make primary backup restore version-aware. Today `install primary --restore-backup true` installs the current executable unless `--version` selects a release, then restores the DB; it does not inspect backup-visible state to restore or warn about the OpenDeploy version that produced the backup.
