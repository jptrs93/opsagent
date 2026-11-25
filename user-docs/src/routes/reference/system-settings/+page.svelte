<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const typeDefinition = `type SystemConfig root {
    id: u8
    settings: type ClusterSettings {
        http_web: type HttpWebSettings {
            enabled: BoolSetting
            listen: StringSetting
        }
        https_web: type HttpsWebSettings {
            enabled: BoolSetting
            listen: StringSetting
            tls_self_managed: BoolSetting
            tls_cert_pem: ?SecretRef
            acme_hosts: StringSetting
            acme_email: StringSetting
        }
        cluster: type ClusterListenSettings {
            listen: StringSetting
            enrollment_listen: StringSetting
        }
        repo: type RepoSettings {
            github_token: ?SecretRef
        }
        backup: type BackupSettings {
            enabled: BoolSetting
            s3_access_key_id: StringSetting
            s3_secret_access_key: ?SecretRef
            s3_bucket: StringSetting
            s3_path: StringSetting
            s3_region: StringSetting
            s3_endpoint: StringSetting
        }
        large_assets: type LargeAssetsSettings {
            use_separate_s3: BoolSetting
            s3_access_key_id: StringSetting
            s3_secret_access_key: ?SecretRef
            s3_bucket: StringSetting
            s3_path: StringSetting
            s3_region: StringSetting
            s3_endpoint: StringSetting
            keep_local_copy: BoolSetting
        }
        auth: type AuthSettings {
            password_login_enabled: BoolSetting
            require_passkey: BoolSetting
        }
        archive: type ArchiveSettings {
            enabled: BoolSetting
            s3_path: StringSetting
        }
    }
    master_password_hash: ?string
    network_ula_prefix: [6]u8
}

type StringSetting {
    value: string | ConfigRef
}

type BoolSetting {
    value: bool | ConfigRef
}
`;
</script>

<svelte:head>
  <title>System settings — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">System settings</h1>

<section id="content" class="page">
<p>System settings are the single record of cluster-wide configuration, and have the following type definition:</p>

<CodeBlock code={typeDefinition} language="typedef" title="SystemConfig" />

<p>There is exactly one record, with id 1, and it is never deleted. Each setting is either a literal or a reference to a <a href="{base}/reference/config-value/">config value</a>, and anything sensitive, a TLS certificate bundle, a GitHub token, or an S3 secret key, is a reference to a <a href="{base}/reference/secret/">secret</a>. The web settings control the HTTP and HTTPS listeners of the Web UI: TLS is self-managed with a locally generated CA, issued through ACME for the listed hosts, or taken from a supplied PEM bundle. The cluster settings are the listeners nodes and enrolling machines connect to. The GitHub token is used for private repositories and container images. Backup, when enabled, copies the cluster state and asset content to the S3 bucket, and the large asset settings can send asset content to a separate bucket and keep a local copy beside it. The archive settings send each node's sealed log and metrics files to the backup bucket under a path of their own, and apply only while backup is enabled; see <a href="{base}/guides/back-up-and-restore/">back up and restore</a>.</p>

<p>Requiring a passkey turns password sign-in off for every user; the older password login flag is kept for the configuration history and no longer read. The master password hash is what a root session is checked against, and the ULA prefix is the IPv6 /48 the cluster's virtual network is numbered from, generated once at installation and never changed.</p>
</section>
