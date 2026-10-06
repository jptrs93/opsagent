type Asset@6 root {
    id@1: u64 [key]
    fs@2: type AssetFs {
        key@1: string [max_length = 255, min_length = 1]
        directory_id@2: ?AssetDirectory.id
        laws {
            file_name {
                forbid key in [".", ".."]
                forbid key.contains("/") || key.contains("\\") || key.contains("\u0000")
            }
        }
    }
    space_id@3: Space.id
    size_bytes@4: u64
    sha256@5: [32]u8
    storage_key@6: string [min_length = 1]
    laws {
        directory_in_space {
            assert fs.directory_id is absent || fs.directory_id->space_id == space_id
        }
        asset_sibling_key {
            unique (space_id, fs.directory_id, fs.key)
        }
    }
}

type AssetDirectory@11 root {
    id@1: u64 [key]
    space_id@2: Space.id [mutable = false]
    key@3: string [max_length = 255, min_length = 1]
    parent_id@4: ?AssetDirectory.id
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
        asset_directory_sibling_key {
            unique (space_id, parent_id, key)
        }
    }
}

laws asset_names {
    asset_directory_keys (a: Asset, d: AssetDirectory, space: Space) where a.space_id == space.id && d.space_id == space.id && a.fs.directory_id == d.parent_id {
        assert a.fs.key != d.key
    }
}
