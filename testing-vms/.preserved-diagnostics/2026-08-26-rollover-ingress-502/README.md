# rollover-ingress 502 — missing service-address route (2026-08-26, commit 556e90a)

Case `create-https-ingress-rollover-deployment` (44/130) failed: ingress served
502 for the full 180s poll.

Root cause evidence (captured live, VMs still up at capture time):

- Workload IS healthy: `rollover` listening on *:8080 in netns `opendeploy-18-v2`,
  and a direct dial of its INSTANCE address returns 200 with the correct body
  `rollover generation=ingress-v1`.
- The STABLE SERVICE address has no route installed, so dialing it is refused.
  The netproxy dials the service address -> 502.

    instance  fd37:ee1:8248:1:0:1200:0:2001:8080 -> 200 "rollover generation=ingress-v1"
    service   fd37:ee1:8248:1:0:1200:::8080      -> exit=7 refused (no route)

- Every OTHER deployment on worker-2 has BOTH routes; 18 has only the instance one:

    fd37:ee1:8248:1:0:d00::        dev od13s0    <- service
    fd37:ee1:8248:1:0:d00:0:1301   dev od13s0    <- instance
    fd37:ee1:8248:1:0:1200:0:2001  dev od18s1    <- instance only, NO service route

- Deployment 18's veth is `od18s1` (slot 1 = second version); all others are s0.
  So this deployment is the only one that actually rolled over, and the service
  address route was not moved/installed onto the new slot.

NOT the ssh-tunnel flake: tunnel-probes.txt here shows the ingress alive
(exit=35 TLS on :443, 301 on :80), unlike the tunnel-stall captures.
