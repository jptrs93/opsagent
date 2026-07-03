# Todo

## Backlog

- Clean up Git repository, branch, commit, and flake path validation. The current implementation is overcomplicated.
- Improve validation on resource create and update operations so the system strictly controls data validity at all times.
- Optimize internal asset storage. Consider content hashes and compression.
- Add a log housekeeper that compresses and backs up workload logs. Show total log space used.
- Review the system design for immutability and versioning of assets, configs, and secrets. Define the behavior for auto-upgrading deployments that depend on changed assets, configs, or secrets.
- Add "used by" information for assets, configs, secrets, and spaces. Show how many deployments use each item and which deployments use it.
- Expand the machines page to show more machine details, including network interfaces, IP addresses, CPU cores, architecture, RAM, and disk space.
- Major feature: evaluate how to integrate networking, VPN, and service mesh capabilities.
- Major feature: add resource monitoring for workloads.
- Add resource requests and limits to deployment configuration.
- Major feature: allow deployments to be defined without specifying a machine. Add a scheduler that automatically schedules deployments onto machines.
- Review Litestream behavior when an existing remote backup is ahead of the local primary database. Define how OpenDeploy should detect, warn, block, or recover before a stale local database can publish a newer backup snapshot.
- Make primary backup restore version-aware. Today `install primary --restore-backup true` installs the selected/latest OpenDeploy binary and then restores the DB; it does not inspect backup-visible state to restore or warn about the OpenDeploy version that produced the backup.
