type NetworkPolicy@7 root {
    id@1: u64 [key]
    action@2: enum NetworkPolicyAction {
        Allow@1
    }
    source@3: type NetworkPolicyPeer {
        target@1: type SpacePeer@1 {
            space_id@1: Space.id
        } | type DeploymentPeer@2 {
            deployment_id@1: Deployment.id
        }
    }
    destination@4: NetworkPolicy.NetworkPolicyPeer
    ports@5: []type NetPortMatch {
        protocol@1: enum NetProtocol {
            Tcp@1
            Udp@2
        }
        range@2: type PortRange {
            start@1: u32 [max = 65535, min = 1]
            end@2: u32 [max = 65535, min = 1]
            laws {
                ordered {
                    assert start <= end
                }
            }
        }
    }
}
