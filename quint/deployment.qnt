module deployment {

  type DeploymentId = int
  type InstanceId = int
  type NodeId = int
  type SpaceId = int
  type Ordinal = int
  type Version = int
  type Seq = int
  type Port = int
  type ServiceName = str
  type Hostname = str
  type UnderlayAddress = str

  type Strategy = Rollover | Recreate

  type Deployment = {
    id: DeploymentId,
    name: ServiceName,
    space: SpaceId,
    node: NodeId,
    version: Version,
    strategy: Strategy,
    deleted: bool,
  }

  type Target = Serving | Standby | Draining | Terminate | Finalized
  type Runner = Unknown | Starting | Running | Stopped | Crashed

  type ScheduledInstance = {
    id: InstanceId,
    deployment: DeploymentId,
    deploymentVersion: Version,
    node: NodeId,
    ordinal: Ordinal,
    target: Target,
    runner: Runner,
    seq: Seq,
  }

  type InboundAddress = {
    space: SpaceId,
    deployment: DeploymentId,
    ordinal: Ordinal,
  }

  type RoutedPrefix =
    InstancePrefix(InboundAddress) |
    PlacementPrefix(InstanceId)

  type Route = {
    prefix: RoutedPrefix,
    hostingNode: NodeId,
  }

  type GlobalNetMap = {
    targetNode: NodeId,
    nodes: NodeId -> UnderlayAddress,
    routes: Set[Route],
    derivedFromSeq: Seq,
  }

  type Endpoint = {
    address: InboundAddress,
    port: Port,
  }

  type DnsService = {
    name: ServiceName,
    space: SpaceId,
    endpoints: Set[InboundAddress],
  }

  type Ingress =
    TlsPassthrough({ hostname: Hostname, hostPort: Port, backends: Set[Endpoint] }) |
    Https({ hostname: Hostname, backends: Set[Endpoint] })

  type LocalNetMap = {
    seq: Seq,
    node: NodeId,
    dnsServices: Set[DnsService],
    ingress: Set[Ingress],
  }
}
