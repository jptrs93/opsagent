type SystemConfig@15 root {
    id@1: u8 [key, max = 1, min = 1]
    settings@2: type ClusterSettings {
        http_web@1: type HttpWebSettings {
            enabled@1: BoolSetting
            listen@2: StringSetting
        }
        https_web@2: type HttpsWebSettings {
            enabled@1: BoolSetting
            listen@2: StringSetting
            tls_self_managed@3: BoolSetting
            tls_cert_pem@4: ?SecretRef
            acme_hosts@5: StringSetting
            acme_email@6: StringSetting
        }
        cluster@3: type ClusterListenSettings {
            listen@1: StringSetting
            enrollment_listen@2: StringSetting
        }
        repo@4: type RepoSettings {
            github_token@1: ?SecretRef
        }
        backup@5: type BackupSettings {
            enabled@1: BoolSetting
            s3_access_key_id@2: StringSetting
            s3_secret_access_key@3: ?SecretRef
            s3_bucket@4: StringSetting
            s3_path@5: StringSetting
            s3_region@6: StringSetting
            s3_endpoint@7: StringSetting
        }
        large_assets@6: type LargeAssetsSettings {
            use_separate_s3@1: BoolSetting
            s3_access_key_id@2: StringSetting
            s3_secret_access_key@3: ?SecretRef
            s3_bucket@4: StringSetting
            s3_path@5: StringSetting
            s3_region@6: StringSetting
            s3_endpoint@7: StringSetting
            keep_local_copy@8: BoolSetting
        }
        auth@7: type AuthSettings {
            password_login_enabled@1: BoolSetting
        }
    }
    master_password_hash@3: ?string [min_length = 1]
    network_ula_prefix@4: [6]u8 [mutable = false]
    laws {
        locally_assigned_ula {
            assert network_ula_prefix[0] == 253
        }
        never_deleted on delete {
            forbid true
        }
    }
}

type NixStoreReset@20 root {
    id@1: u64 [key]
    repo@2: string [max_length = 512, min_length = 1, mutable = false]
    laws {
        nix_store_reset_repo {
            unique (repo)
        }
        never_deleted on delete {
            forbid true
        }
    }
}

type StringSetting {
    value@1: string@1 | ConfigRef@2
}

type BoolSetting {
    value@1: bool@1 | ConfigRef@2
}
