<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const restore = `# Set S3_SECRET_ACCESS_KEY and RECOVERY_CODE before running it.
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/restore_primary.sh | bash -s -- \\
  --web-hosts opendeploy.example.com \\
  --restore-backup true \\
  --restore-s3-access-key-id AKIAIOSFODNN7EXAMPLE \\
  --restore-s3-secret-access-key "$S3_SECRET_ACCESS_KEY" \\
  --restore-s3-bucket opendeploy-backups \\
  --restore-s3-path opendeploy/primary \\
  --restore-s3-region us-east-1 \\
  --recovery-code "$RECOVERY_CODE"
`;
</script>

<svelte:head>
  <title>Back up and restore — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Back up and restore</h1>
<p class="lead">Replicate the primary's state to an S3 bucket as it changes, and bring a cluster back on a new machine if the primary is lost.</p>

<!-- ============================================================ -->
<section id="what" class="page">
<h2>What is backed up</h2>

<p>Everything the cluster is configured with lives in one database on the primary: spaces, deployments and their history, secrets, configs, users, policies, and settings. With backup enabled the primary streams every change in that database to the bucket within seconds, continuously rather than on a schedule, and uploaded assets are stored in the bucket as well. Secondary nodes hold nothing that is not refetched from the primary, so they need no backup of their own.</p>

<p>What is not backed up is the data deployments write to their volumes. A volume belongs to the node the deployment runs on, and a deployment that must survive the loss of that node needs to keep its state elsewhere, in a database with its own backups or in object storage.</p>
</section>

<!-- ============================================================ -->
<section id="enable" class="page">
<h2>Enable backup</h2>

<p>You need a bucket and a key pair that can read, write, and list in it. Any S3 compatible service works; for one other than AWS, fill in its endpoint. Open <strong>Settings</strong>, <strong>Backup</strong>, enter the credentials, bucket, path, and region, turn <strong>Backup enabled</strong> on, and save.</p>

<p>Replication starts at once. Open <strong>Nodes</strong> and the <strong>Replication</strong> tab to watch it: the status reads <em>syncing</em> until the bucket has caught up and <em>in sync</em> afterwards, with the time of the last sync. Existing assets move to the bucket in the background and the tab counts down the ones still pending. Once it reads in sync, a copy of every change reaches the bucket moments after it is made.</p>

<p>Keep the bucket's lifecycle policy in mind: OpenDeploy does not delete replaced asset content from the bucket, so a policy that expires old objects keeps it tidy. Set it to keep objects for longer than you would ever restore back to.</p>
</section>

<!-- ============================================================ -->
<section id="recovery" class="page">
<h2>Keep what a restore needs</h2>

<p>Secrets in the backup are encrypted with a key that never leaves the primary machine. The only other way to open them is the recovery code: generate it under <strong>Settings</strong>, <strong>Secrets recovery</strong>, and store it with your other credentials. It is shown once, and without it a restored cluster has configs and deployments but no secrets. The same section shows a <strong>Restore command</strong> filled in from the current settings; copy it somewhere outside the cluster too, along with the S3 credentials.</p>

<p>Do this when you enable backup, not when you need it. A backup whose recovery code was never written down cannot be fully restored.</p>
</section>

<!-- ============================================================ -->
<section id="restore" class="page">
<h2>Restore onto a new machine</h2>

<p>Prepare a fresh machine as you would for an <a href="{base}/guides/getting-started/#install">install</a>, and point the primary's DNS name at it so that browsers, secondary nodes, and certificate issuance find it. Then run the restore command you saved, with the S3 secret and the recovery code in the environment. It takes the same options as the installer, so an install that used its own certificate passes the certificate flag again here.</p>

<CodeBlock code={restore} language="text" />

<p>The command installs the agent, downloads the latest state of the database from the bucket, unlocks the secrets with the recovery code, seals them with a new machine key, and starts the service. The cluster comes back as it was: the same users and master password, the same deployments, and the same certificates. Deployments assigned to the primary are prepared again from their saved configs, since the old machine's containers are gone, while deployments on secondary nodes keep running throughout and the nodes reconnect on their own once the name resolves to the new machine. Assets stay in the bucket, where the new primary reads them.</p>

<p>The restore takes the newest state in the bucket. Going back to an earlier point in time is not supported, because asset content that only older versions referenced may already have expired. A restore refuses to run on a machine that already has a primary database.</p>
</section>
