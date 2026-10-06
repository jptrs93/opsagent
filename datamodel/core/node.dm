type Node@3 root {
    id@1: u64 [key]
    status@2: enum NodeLifecycleStatus {
        reserve @0
        EnrollmentRequested@1
        EnrollmentCancelled@2
        EnrollmentRequestExpired@3
        MemberNormal@4
        MemberUnhealthy@5
        MemberDraining@6
        MemberMissing@7
        MemberEvicted@8
    }
    enrollment_requested_at@3: ?i64
    operator@4: type NodeOperator {
        name@1: string
        roles@2: []enum NodeRole {
            Primary@0
            Secondary@1
        }
        allowed_spaces@3: []Space.id
        enrolled_time@4: ?i64
    }
    reported@5: type NodeReported {
        identifier@1: string
        underlay_address@2: IPv4Address@1 | IPv6Address@2
        wg_public_key@3: string
        host_addresses@4: [](IPv4Address@1 | IPv6Address@2)
        host_addresses_unknown@5: bool
    }
}

type NodeStatus@17 root {
    node_id@1: Node.id [key]
    updated_at@2: i64
    is_connected@3: bool
    last_connected_at@4: ?i64
    remote_address@5: string
    opendeploy_version@6: string
    runtime_versions@7: string
    laws {
        clock_monotonic on update {
            assert after.updated_at >= before.updated_at
        }
    }
}
