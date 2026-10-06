type Secret@4 root {
    id@1: u64 [key]
    fs@2: type SecretFs {
        key@1: string [max_length = 255, min_length = 1]
        directory_id@2: ?ValueDirectory.id
        laws {
            file_name {
                forbid key in [".", ".."]
                forbid key.contains("/") || key.contains("\\") || key.contains("\u0000")
            }
        }
    }
    space_id@3: Space.id
    sealed@4: ?type SealedSecret {
        smk_version@1: u32 [min = 1]
        ciphertext@2: bytes [min_length = 1]
        nonce@3: bytes [min_length = 1]
    }
    laws {
        directory_in_space {
            assert fs.directory_id is absent || fs.directory_id->space_id == space_id
        }
        secret_sibling_key {
            unique (space_id, fs.directory_id, fs.key)
        }
    }
}

type Config@5 root {
    id@1: u64 [key]
    fs@2: type ConfigFs {
        key@1: string [max_length = 255, min_length = 1]
        directory_id@2: ?ValueDirectory.id
        laws {
            file_name {
                forbid key in [".", ".."]
                forbid key.contains("/") || key.contains("\\") || key.contains("\u0000")
            }
        }
    }
    space_id@3: Space.id
    value@4: string
    laws {
        directory_in_space {
            assert fs.directory_id is absent || fs.directory_id->space_id == space_id
        }
        config_sibling_key {
            unique (space_id, fs.directory_id, fs.key)
        }
    }
}

type ValueDirectory@10 root {
    id@1: u64 [key]
    space_id@2: Space.id [mutable = false]
    key@3: string [max_length = 255, min_length = 1]
    parent_id@4: ?ValueDirectory.id
    laws {
        file_name {
            forbid key in [".", ".."]
            forbid key.contains("/") || key.contains("\\") || key.contains("\u0000")
        }
        parent_in_space {
            assert parent_id is absent || parent_id->space_id == space_id
        }
        not_own_parent {
            forbid parent_id == id
        }
        value_directory_sibling_key {
            unique (space_id, parent_id, key)
        }
    }
}

laws filesystem_keys {
    secret_config_keys (s: Secret, c: Config, space: Space) where s.space_id == space.id && c.space_id == space.id && s.fs.directory_id == c.fs.directory_id {
        assert s.fs.key != c.fs.key
    }
    secret_directory_keys (s: Secret, d: ValueDirectory, space: Space) where s.space_id == space.id && d.space_id == space.id && s.fs.directory_id == d.parent_id {
        assert s.fs.key != d.key
    }
    config_directory_keys (c: Config, d: ValueDirectory, space: Space) where c.space_id == space.id && d.space_id == space.id && c.fs.directory_id == d.parent_id {
        assert c.fs.key != d.key
    }
}
