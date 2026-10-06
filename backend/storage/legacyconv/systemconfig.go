package legacyconv

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

// systemConfigID is the single system config row's id, which the new model
// carries in the payload and the validator pins to 1 (pq.SystemConfigEntityID).
const systemConfigID uint32 = 1

// SystemConfig resolves the settings unions (item 19): a set config reference
// wins over the literal beside it, and a secret setting is present only when
// the old reference named a secret.
func SystemConfig(old *apigenold.SystemConfig) (*apigen.SystemConfig, error) {
	c := &conv{}
	return checked("SystemConfig", c.systemConfig(old), c.err)
}

// systemConfig fills an empty network_ula_prefix on a historic row from the
// latest revision's prefix: the field is immutable, so every revision carries
// the same bytes and the early rows merely predate the networking init.
func (c *conv) systemConfig(old *apigenold.SystemConfig) *apigen.SystemConfig {
	if old == nil {
		c.refuse("SystemConfig", "", "nil payload")
		return nil
	}
	prefix := old.NetworkUlaPrefix
	if len(prefix) == 0 && c.historic && len(c.ulaPrefix) > 0 {
		prefix = c.ulaPrefix
		c.repair("SystemConfig", "network_ula_prefix", "empty, took the latest revision's prefix")
	}
	return &apigen.SystemConfig{
		ID:                 systemConfigID,
		Settings:           c.clusterSettings(&old.Settings),
		MasterPasswordHash: optString(old.MasterPasswordHash),
		NetworkUlaPrefix:   prefix,
	}
}

func (c *conv) clusterSettings(old *apigenold.ClusterSettings) apigen.ClusterSettings {
	return apigen.ClusterSettings{
		HttpWeb: apigen.HttpWebSettings{
			Enabled: c.boolSetting("HttpWebSettings", "enabled", old.HttpWeb.Enabled),
			Listen:  c.stringSetting("HttpWebSettings", "listen", old.HttpWeb.Listen),
		},
		HttpsWeb: apigen.HttpsWebSettings{
			Enabled:        c.boolSetting("HttpsWebSettings", "enabled", old.HttpsWeb.Enabled),
			Listen:         c.stringSetting("HttpsWebSettings", "listen", old.HttpsWeb.Listen),
			TlsSelfManaged: c.boolSetting("HttpsWebSettings", "tls_self_managed", old.HttpsWeb.TlsSelfManaged),
			TlsCertPem:     c.secretSetting("HttpsWebSettings", "tls_cert_pem", old.HttpsWeb.TlsCertPem),
			AcmeHosts:      c.stringSetting("HttpsWebSettings", "acme_hosts", old.HttpsWeb.AcmeHosts),
			AcmeEmail:      c.stringSetting("HttpsWebSettings", "acme_email", old.HttpsWeb.AcmeEmail),
		},
		Cluster: apigen.ClusterListenSettings{
			Listen:           c.stringSetting("ClusterListenSettings", "listen", old.Cluster.Listen),
			EnrollmentListen: c.stringSetting("ClusterListenSettings", "enrollment_listen", old.Cluster.EnrollmentListen),
		},
		Repo: apigen.RepoSettings{
			GithubToken: c.secretSetting("RepoSettings", "github_token", old.Repo.GithubToken),
		},
		Backup: apigen.BackupSettings{
			Enabled:           c.boolSetting("BackupSettings", "enabled", old.Backup.Enabled),
			S3AccessKeyID:     c.stringSetting("BackupSettings", "s3_access_key_id", old.Backup.S3AccessKeyID),
			S3SecretAccessKey: c.secretSetting("BackupSettings", "s3_secret_access_key", old.Backup.S3SecretAccessKey),
			S3Bucket:          c.stringSetting("BackupSettings", "s3_bucket", old.Backup.S3Bucket),
			S3Path:            c.stringSetting("BackupSettings", "s3_path", old.Backup.S3Path),
			S3Region:          c.stringSetting("BackupSettings", "s3_region", old.Backup.S3Region),
			S3Endpoint:        c.stringSetting("BackupSettings", "s3_endpoint", old.Backup.S3Endpoint),
		},
		LargeAssets: apigen.LargeAssetsSettings{
			UseSeparateS3:     c.boolSetting("LargeAssetsSettings", "use_separate_s3", old.LargeAssets.UseSeparateS3),
			S3AccessKeyID:     c.stringSetting("LargeAssetsSettings", "s3_access_key_id", old.LargeAssets.S3AccessKeyID),
			S3SecretAccessKey: c.secretSetting("LargeAssetsSettings", "s3_secret_access_key", old.LargeAssets.S3SecretAccessKey),
			S3Bucket:          c.stringSetting("LargeAssetsSettings", "s3_bucket", old.LargeAssets.S3Bucket),
			S3Path:            c.stringSetting("LargeAssetsSettings", "s3_path", old.LargeAssets.S3Path),
			S3Region:          c.stringSetting("LargeAssetsSettings", "s3_region", old.LargeAssets.S3Region),
			S3Endpoint:        c.stringSetting("LargeAssetsSettings", "s3_endpoint", old.LargeAssets.S3Endpoint),
			KeepLocalCopy:     c.boolSetting("LargeAssetsSettings", "keep_local_copy", old.LargeAssets.KeepLocalCopy),
		},
		Auth: apigen.AuthSettings{
			PasswordLoginEnabled: c.boolSetting("AuthSettings", "password_login_enabled", old.Auth.PasswordLoginEnabled),
		},
	}
}

func (c *conv) stringSetting(typ, field string, old apigenold.StringSetting) apigen.StringSetting {
	if old.ConfigRef.Ref.ID != 0 {
		ref := c.configRef(typ, field+".config_ref", &old.ConfigRef.Ref)
		return apigen.StringSetting{Value: apigen.StringSettingValue{Value: apigen.StringSettingValueValueOneof{ConfigRef: &ref}}}
	}
	v := old.Value
	return apigen.StringSetting{Value: apigen.StringSettingValue{Value: apigen.StringSettingValueValueOneof{Literal: &v}}}
}

func (c *conv) boolSetting(typ, field string, old apigenold.BoolSetting) apigen.BoolSetting {
	if old.ConfigRef.Ref.ID != 0 {
		ref := c.configRef(typ, field+".config_ref", &old.ConfigRef.Ref)
		return apigen.BoolSetting{Value: apigen.BoolSettingValue{Value: apigen.BoolSettingValueValueOneof{ConfigRef: &ref}}}
	}
	v := old.Value
	return apigen.BoolSetting{Value: apigen.BoolSettingValue{Value: apigen.BoolSettingValueValueOneof{Literal: &v}}}
}

func (c *conv) secretSetting(typ, field string, old apigenold.SecretRef) apigen.Maybe[apigen.SecretRef] {
	if old.Ref.ID == 0 {
		return apigen.Maybe[apigen.SecretRef]{}
	}
	return apigen.Some(c.secretRef(typ, field, &old.Ref))
}
