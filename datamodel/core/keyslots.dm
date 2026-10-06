type SecretKeyslot@21 root {
    id@1: u64 [key]
    smk_version@2: u32 [min = 1]
    wrapped_smk@3: bytes [min_length = 1]
    nonce@4: bytes [min_length = 1]
    wrapping@5: type MachineKey@1 {
        node_id@1: Node.id
    } | type RecoveryCode@2 {
        kdf_salt@1: bytes [min_length = 1]
    }
    laws {
        recovery_slot_kept on delete {
            forbid before.wrapping is RecoveryCode
        }
    }
}
