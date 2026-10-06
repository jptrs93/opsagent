type User@9 root {
    id@1: u64 [key]
    name@2: string [min_length = 1]
    authentication@3: type UserAuthentication {
        web_authn_id@1: bytes [mutable = false]
        credentials@2: []type WebAuthnCredential {
            id@1: bytes [min_length = 1]
            data@2: bytes [min_length = 1]
        }
    }
    laws {
        user_name {
            unique (name)
        }
        credential_ids_distinct {
            assert all c in authentication.credentials : (count d in authentication.credentials where d.id == c.id) == 1
        }
        credential_ids_unique [scan = true] {
            forbid any x in authentication.credentials : any b in User where b.id != id : any y in b.authentication.credentials : x.id == y.id
        }
    }
}

type AgentSession@18 root {
    id@1: u64 [key]
    session_id@2: string [min_length = 1, mutable = false]
    user_id@3: User.id [mutable = false]
    status@4: enum AgentSessionStatus {
        Pending@1
        Approved@2
        Rejected@3
        Revoked@4
        transitions {
            start -> Pending | Approved
            Pending -> Approved | Rejected
            Approved -> Rejected | Revoked
        }
    }
    approved_at@5: ?i64
    requesting_address@6: string [mutable = false]
    approval_code@7: ?string [min_length = 1, mutable = false]
    token@8: ?type AgentToken {
        hash@1: ?bytes [min_length = 1]
        prefix@2: string [min_length = 1]
        expires_at@3: i64
    }
    laws {
        agent_session_id {
            unique (session_id)
        }
        token_follows_approval {
            assert token is absent || status == Approved || status == Revoked
        }
        approved_at_follows_status {
            assert approved_at is absent || status != Pending
            assert approved_at is present || status == Pending || status == Rejected
        }
        one_pending_request where status == Pending {
            unique (user_id)
        }
        token_minted_once on update {
            assert before.token is absent || before.token == after.token
        }
        never_deleted on delete {
            forbid true
        }
    }
}

type UserSession@19 root {
    id@1: u64 [key]
    session_id@2: string [min_length = 1, mutable = false]
    user_id@3: User.id [mutable = false]
    kind@4: enum UserSessionKind {
        Full@0
        Bootstrap@1
    } [mutable = false]
    expires_at@5: i64 [mutable = false]
    revoked_at@6: ?i64
    requesting_address@7: string [mutable = false]
    user_agent@8: string [mutable = false]
    token_hash@9: ?bytes [min_length = 1, mutable = false]
    laws {
        user_session_id {
            unique (session_id)
        }
        revoked_once on update {
            assert before.revoked_at is absent || before.revoked_at == after.revoked_at
        }
        never_deleted on delete {
            forbid true
        }
    }
}
