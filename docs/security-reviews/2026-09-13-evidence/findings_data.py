"""Finding data for the 13 September 2026 re-review, as updated on 22 and 23 November 2026.

origin: "audit" keeps the 11 September identifier (OD-nn); "review" items were
raised in the 12 September assessment; "new" items were found during the
13 September re-review; "recheck" items were raised on 22 November. RR-nn
identifiers are assigned here for the latter three.

History of this file:
- 13 September: written against commit 2ea3af1.
- 16 September: OD-07 moved to CLOSED after v0.0.609 (60d73bc).
- 22 November: every open finding re-verified at ef1f3508 (v0.0.617 plus the
  unreleased per-user-password work on bckup-1). The FINDINGS list below keeps
  the 13 September wording; RECHECK_UPDATES at the end of the file overrides
  the fields that are no longer accurate, re-pins every source range to
  ef1f3508 and adds a `recheck` note per finding. RR-13 and the duplicate-row
  half of OD-12 moved to CLOSED, and RR-27 to RR-37 were added.
- 23 November: RR-27, RR-28 and most of RR-05 fixed in commits ca3ff94,
  12f2d8c and fa23e93. RR-27 moved to CLOSED; RR-28 stays open until a release carries
  the fix; RR-05 narrowed from High to Medium with the fixed part in CLOSED.
  See FIX_UPDATES and FIX_CLOSED near the end of the file.

After the 22 November update, source ranges of open findings refer to ef1f3508,
except sources marked FIX_COMMIT, which refer to that commit of 23 November.
Closed and accepted rows keep the commit they were recorded against.
"""

def f(**kw):
    kw.setdefault("refs", [])
    kw.setdefault("prior", "")
    return kw


FINDINGS = [
# ---------------------------------------------------------------- carried from the audit
f(id="OD-02", origin="audit", severity="Low", prior="Critical", area="Host filesystem / authorization", status="Source verified; residual",
  title="Host mount validation stays lexical: symlinked sources are not resolved on the node",
  cwe="CWE-59",
  impact="The Critical path is closed: parents of protected roots and / are rejected, and custom host mounts now require use_host_mounts, which only cluster_admin holds. What remains is that a permitted host path is never resolved on the node before it is bind-mounted, so a symlink under an allowed directory still reaches a protected tree.",
  trigger="A caller with use_host_mounts (cluster_admin by default) and a pre-existing symlink on the target node from an allowed path into a protected one. Both prerequisites are ones the design already treats as host-administrator trust.",
  evidence="containerHostMountDenied cleans the path and checks both directions against the denylist, and its comment states the check is lexical and that administrators own symlinks. The runner passes HostPath to containerd unchanged; there is no EvalSymlinks, openat2 or RESOLVE_BENEATH on the mount source anywhere in the runner.",
  validation="The audit's validator reproduction now fails on /var (\"host path \\\"/var\\\" is not allowed\"). A corrected handler test shows space_admin is denied a custom mount even for an unprotected path. The symlink case was traced in source only.",
  fix="Resolve the source with EvalSymlinks on the node at mount time and re-apply the denylist to the resolved path, or open it with openat2 and RESOLVE_BENEATH relative to the allowed root. Fail the run rather than silently mount.",
  close="A symlink from an allowed host path into /var/lib/opendeploy must fail the run with a clear error; ordinary host mounts must keep working.",
  sources=[["backend/app/primary/domain/deployments/validate.go", 910, 923], ["backend/app/primary/domain/authz/builtin.go", 30, 36]]),

f(id="OD-06", origin="audit", severity="Medium", prior="High", area="Credentials", status="Source verified",
  title="Eligible workers receive the entire configured GitHub token",
  cwe="CWE-269",
  impact="Any node scheduled to run one Nix build or one ghcr.io image obtains the full personal access token, unscoped to repository or node. A compromised worker holds the organisation's token. The hostile-flake path to the token is closed: the build container gets an explicitly constructed environment and never sees it.",
  trigger="Root on a worker, or a workload that escapes to the agent's uid. Eligibility needs only one scheduled instance that uses GitHub.",
  evidence="GetV1ClusterGithubCredentials returns the raw token after a coarse usesGithub check on the node's scheduled instances. The worker caches it in memory. The git manager injects it as a github.com-scoped extraHeader so it never reaches .git/config or argv, and the build container's env is TMPDIR, HOME and the CA bundle only.",
  validation="Handler unchanged since the audit. The containerized build (b6ce56b) and its egress rules were read in full; the e2e hostile-build probe asserts the machine key and containerd socket are absent in the build.",
  fix="Mint a short-lived, repository-scoped installation token per checkout (GitHub App) or keep the token on the primary and have the primary perform fetches, shipping only the checkout to workers.",
  close="A worker must be able to fetch only the repositories of its scheduled deployments, and a captured credential must expire within hours.",
  sources=[["backend/app/primary/clusterhandler/handler.go", 153, 165], ["backend/lib/engine/prepare/nixstore/build.go", 88, 92]]),

f(id="OD-09", origin="audit", severity="Medium", area="Authentication / availability", status="Source verified",
  title="Public password verification has no global memory or concurrency budget",
  cwe="CWE-770",
  impact="Each master-password or password-login attempt costs a 64 MiB Argon2id hash. Per-address rate limits exist and key on the socket address, but nothing bounds aggregate concurrency, and the limiter keys on the exact address, so an IPv6 /64 supplies unlimited independent buckets. The master endpoint is reachable even when password login is disabled.",
  trigger="Unauthenticated access to the web listener from many source addresses.",
  evidence="The middleware chain applies PerIP and PerIPAndPrefix limits; ratelimit.Global exists but is unused, and no semaphore wraps VerifyPassword. perIPWithMatcher allocates a limiter per distinct address string with a 60-second TTL. PostV1AuthMaster is NO_AUTH and calls verifyMasterPassword before any other check.",
  validation="Rate limits and clientaddr were already present at the audited commit; nothing changed. Load impact was not measured.",
  fix="Wrap password verification in a global semaphore sized for the node, key rate limits on a /64 for IPv6 and a /24 for IPv4, and return 503 rather than queueing when the budget is exhausted.",
  close="A flood from many addresses must not raise the primary's resident memory beyond the semaphore's bound.",
  sources=[["backend/app/primary/run.go", 140, 150], ["backend/lib/middleware/ratelimit/ratelimit.go", 43, 69]]),

f(id="OD-10", origin="audit", severity="Medium", area="Listeners / availability", status="Source verified",
  title="Public listeners lack header and TLS-handshake deadlines",
  cwe="CWE-400",
  impact="The Web UI HTTPS, cluster mTLS and enrollment listeners set no ReadHeaderTimeout, ReadTimeout or WriteTimeout. In Go 1.27 that means no TLS handshake deadline at all, so an unauthenticated peer can hold half-open connections on :443, :9443 and :9444 indefinitely and exhaust goroutines on the primary.",
  trigger="Network reachability of the primary's public ports.",
  evidence="All three http.Server literals carry Handler, TLSConfig and BaseContext only; the cluster server adds HTTP/2 ping timeouts, which act after the handshake. The stdlib derives the handshake deadline from the minimum positive server timeout, so three zeros yield none. Netproxy's HTTPS listener has a 10-second header timeout, but its ClientHello deadline is cleared before hand-off onto an unbuffered channel, leaving an unbounded queueing window.",
  validation="Diffs of webui/server.go, clusterserver/server.go and netproxy against the audited commit are empty.",
  fix="Set ReadHeaderTimeout (10s), IdleTimeout (75s) and a bounded MaxHeaderBytes on every listener; keep the netproxy pre-handshake deadline until Accept, and cap the number of parked handoffs.",
  close="A client that connects and sends nothing must be closed within the header timeout on every public port.",
  sources=[["backend/app/primary/clusterserver/server.go", 42, 51], ["backend/app/primary/clusterserver/server.go", 84, 89], ["backend/app/primary/webui/server.go", 71, 76]]),

f(id="OD-11", origin="audit", severity="Medium", area="Registry client / SSRF", status="Reproduced against HEAD",
  title="Registry-supplied URLs can reach internal services and forward bearer tokens",
  cwe="CWE-918",
  impact="A hostile registry's bearer challenge points the primary at any URL, including plain-HTTP loopback and private addresses, and its Link header re-targets an authenticated pagination request to another origin. The OpenDeploy GitHub token is not at risk: the transport injects it only at ghcr.io/token and strips Authorization elsewhere.",
  trigger="Permission to create a deployment somewhere, which agent sessions hold, and a registry hostname the caller controls.",
  evidence="registryBearerToken parses the realm and issues a GET with no scheme or host restriction. registryGet sets the registry's bearer token on whatever URL nextRegistryPageURL returns, and non-GHCR registries use the default client. The image reference parser accepts any host with a dot or port.",
  validation="Both audit reproductions still pass on loopback. The three files are unchanged since the audit; the PAT scoping in registryauth predates it.",
  fix="Require https for realms, resolve and reject loopback, link-local, RFC1918, ULA and cluster-prefix destinations before dialling, restrict pagination to the registry's own origin, and use the same hardened transport for every registry.",
  close="A realm or Link pointing at 127.0.0.1 or a private range must be refused without a connection attempt.",
  sources=[["backend/lib/engine/versionprovider/containerimage.go", 196, 216], ["backend/lib/engine/versionprovider/containerimage.go", 155, 164], ["backend/lib/engine/registryauth/client.go", 51, 60]]),

f(id="OD-12", origin="audit", severity="Medium", area="Authentication", status="Reproduced against HEAD",
  title="Passkey login updates append duplicate credentials and retain stale state",
  cwe="CWE-287",
  impact="Every successful passkey login appends another copy of the credential to the user record. The verifier picks the first match, so the registration-time sign count and backup flags are compared forever and clone detection never fires; the record grows without bound.",
  trigger="Normal use. No attacker action is needed for the growth; a cloned authenticator is needed to exploit the disabled detection.",
  evidence="The shared save callback appends to d.Credentials rather than replacing the entry with the same ID. It is reached from both FinishRegistration and FinishLogin. CloneWarning is never read anywhere in the backend.",
  validation="The audit reproduction still passes: two saves yield two rows with the stale counter first. passkey.go is unchanged since the audit.",
  fix="Replace the credential whose ID matches in place, persist the updated counter and flags, and surface CloneWarning as a login refusal or an alert.",
  close="Saving the same credential twice must leave one row carrying the latest counter.",
  sources=[["backend/app/primary/webuihandler/passkey.go", 67, 72]]),

f(id="OD-13", origin="audit", severity="Low", prior="Medium", area="Frontend supply chain", status="Lockfile scanner verified",
  title="Frontend build dependencies have outstanding advisories",
  cwe="CWE-1104",
  impact="Fourteen advisories (8 high, 6 moderate) remain, all reached through vite, postcss, nanoid and yaml. None ships in the bundle served to browsers; the exposure is a developer running the dev server. pnpm audit --prod reports the same set because the tailwind vite plugin sits in dependencies.",
  trigger="A developer running pnpm run dev against hostile input, or a build with attacker-controlled CSS source. Neither applies to the deployed Web UI.",
  evidence="The lockfile is unchanged since the audit. The runtime dependency set is codemirror, lezer, bip39 and vanjs, none of which carries an advisory.",
  validation="pnpm audit and pnpm audit --prod were run read-only on 13 September; inventories preserved in the evidence directory.",
  fix="Bump vite to the current 7.x, which pulls fixed postcss and nanoid, and move vite, tailwindcss and @tailwindcss/vite to devDependencies so the production audit is meaningful.",
  close="pnpm audit --prod must report zero advisories.",
  sources=[["frontend/package.json", 14, 32]]),

# ---------------------------------------------------------------- raised in the 12 September assessment
f(id="RR-01", origin="review", severity="High", area="Workload isolation", status="Source verified",
  title="Workload containers have no CPU, memory or pids limits, and /dev/shm is unbounded",
  cwe="CWE-770",
  impact="A single deployment can consume all memory, CPU and process slots on its node and take the OpenDeploy agent, containerd and every co-tenant down with it. Build containers received limits in b6ce56b; workloads did not.",
  trigger="Any deployment permission. No hostile intent is required; a memory leak suffices.",
  evidence="The workload ContainerSpec sets ID, image, user, env, args, cwd, dev-shm size, file-descriptor limit, mounts, log fields and netns, and never populates Resources, so the OCI memory, CPU and pids options are skipped. devShmSizeKb is validated only for non-negativity. No deployment spec field for resource limits exists.",
  validation="container.go is byte-identical to the audited commit. The resource wiring added for builds was read and confirmed to apply only when Resources is non-nil.",
  fix="Give workloads node-default memory, CPU and pids budgets with a per-deployment override in the spec, bound dev_shm_size_kb by the memory budget, and reflect OOM kills in the run report.",
  close="A workload that allocates without bound must be OOM-killed inside its cgroup while the agent and other workloads keep running.",
  sources=[["backend/lib/engine/runner/container.go", 540, 559], ["backend/lib/engine/ctrd/client.go", 323, 333], ["backend/app/primary/domain/deployments/validate.go", 798, 806]]),

f(id="RR-02", origin="review", severity="Medium", area="Workload isolation", status="Source verified",
  title="Workloads run without a seccomp profile, as root, without a user namespace, and with CAP_NET_RAW",
  cwe="CWE-250",
  impact="The kernel attack surface exposed to a workload is larger than the Docker default: the full syscall table, root in the container, containerd's default capability set including CAP_NET_RAW, and no user-namespace boundary. This lowers the bar for every runtime escape, including the class OD-03 tracked.",
  trigger="Any deployment permission and a hostile or compromised image.",
  evidence="DefaultSeccomp is set only on build and maintenance specs, so seccomp.WithDefaultProfile is never applied to workloads. No WithUserNamespace, uid mapping, AppArmor or capability option appears in the workload path; the spec can set a user but nothing requires one.",
  validation="Traced buildSpecOpts and the workload spec construction; no test executes an escape.",
  fix="Apply the default seccomp profile to workloads, drop CAP_NET_RAW and other unneeded capabilities, default to a non-root user when the image does not set one, and evaluate user namespaces once containerd's idmapped mount support is confirmed on target kernels.",
  close="A workload must be unable to open a raw socket or call a syscall outside the default profile.",
  sources=[["backend/lib/engine/ctrd/client.go", 335, 337]]),

f(id="RR-03", origin="review", severity="Medium", area="Volumes / authorization", status="Source verified",
  title="Cross-deployment mounts give read-write access to default-space volumes with no authorization check",
  cwe="CWE-285",
  impact="A deployment in any space can mount, read-write, the default volume of any same-node deployment in the default space, and an agent session can do it too. The validator checks locality only and its error messages return the source deployment's name and space to callers who may not be entitled to see it.",
  trigger="Create or update permission in any space on a node that also hosts default-space deployments.",
  evidence="validateCrossDeploymentMountSources allows a source in the caller's space or the default space, on the same node, with a default volume. No verb is checked on the source deployment, and use_host_mounts inspects only Runtime.Mounts. The runner honours READ_WRITE.",
  validation="Unchanged since the audit. The exempted space is DefaultSpaceID (1), the global space; the system space is 0.",
  fix="Require view (or a dedicated mount verb) on the source deployment in its own space, default cross-space mounts to read-only, and drop the name from the refusal message for sources the caller cannot see.",
  close="A caller without access to the source deployment must be refused with a message that does not name it.",
  sources=[["backend/app/primary/domain/deployments/validate.go", 926, 953]]),

f(id="RR-04", origin="review", severity="Medium", prior="High", area="Host networking / policy", status="Source verified",
  title="Host-network deployments bypass network policy and keep CAP_NET_RAW",
  cwe="CWE-284",
  impact="Selecting host networking now requires use_host_network, held by cluster_admin only, which is why the severity drops. The isolation gap itself is unchanged: a host-mode container shares the host netns, so it reaches every loopback listener, every other space's and node's workload addresses, and can sniff with CAP_NET_RAW. No nftables rule can match it.",
  trigger="use_host_network, or an operator-authored wildcard template that inherited it (RR-21).",
  evidence="The gate is enforced on create, v2 update including the saved spec, and space moves, and agent rules exclude the verb. The new input chain matches only the build interface set and its policy is accept; host-mode containers have no veth to match on. buildSpecOpts sets no capability option, so containerd's default set applies.",
  validation="Gate tests pass. Policy behaviour was traced in source; no Linux enforcement test was run.",
  fix="Document host mode as host-administrator trust, drop CAP_NET_RAW from host-mode workloads, and consider an input-chain rule set keyed on the workload's uid or cgroup for the listeners it must not reach.",
  close="A host-mode workload must be unable to reach the enrollment and cluster listeners on loopback.",
  sources=[["backend/app/primary/webuihandler/deployments.go", 100, 102], ["backend/lib/network/filter.go", 144, 155]]),

f(id="RR-05", origin="review", severity="High", area="Host ports / ingress plan", status="Source verified",
  title="Port forwards can claim reserved and co-tenant host ports",
  cwe="CWE-284 / CWE-362",
  impact="Any deployer in any space can bind 443, 80, 22 or the cluster ports on the target node, or a port another tenant already forwards. The lower deployment id wins silently, UDP is never checked, and the DNAT rule matches any local address including loopback, so the forward also captures traffic to 127.0.0.1.",
  trigger="Create or update permission in any space.",
  evidence="validatePortForwarding checks range and duplicates within one spec. matchReservation is called only inside the ingress-route loop, and the TCP port map records the first owner without a diagnostic and is consulted only for ingress claims. HostPortRule for a forward carries no Dest, so addDNATRules falls back to fib daddr type local.",
  validation="ingressplan and the runner's port-forward code are unchanged since the audit; no VM test was run.",
  fix="Run port forwards through the same reservation and claim machinery as ingress routes, include UDP, reject collisions at write time, and restrict the DNAT destination to the node's published addresses.",
  close="A forward on a reserved port or a port another deployment already forwards must be rejected at write time.",
  sources=[["backend/app/primary/domain/deployments/validate.go", 183, 209], ["backend/lib/ingressplan/ingressplan.go", 158, 171], ["backend/lib/engine/runner/container.go", 1366, 1377]]),

f(id="RR-06", origin="review", severity="Medium", area="Ingress proxy", status="Source verified",
  title="Ingress path-prefix routing forwards un-normalised paths across tenant prefixes",
  cwe="CWE-22 / CWE-436",
  impact="Prefix matching runs on the decoded request path without cleaning, and forwarding sends either the raw bytes or a decoded-and-stripped path. /api/../secret routes on the /api prefix and reaches that prefix's backend, which will normalise it to /secret. Because each prefix is a cross-deployment claim, the routing decision and the served resource can disagree across a tenant boundary.",
  trigger="An HTTPS ingress hostname shared by two prefixes owned by different deployments, and an upstream that normalises paths.",
  evidence="routePrefixMatch is HasPrefix plus a segment-boundary check on r.URL.Path. The handler is the server itself, so no ServeMux cleaning applies. With strip enabled the trim operates on the decoded path and RawPath is discarded; without it the raw path is forwarded byte for byte. Route definitions are cleaned at admission; requests are not.",
  validation="netproxy is unchanged since the audit. No fuzzing of encoded or dot-segment paths was run.",
  fix="Reject requests whose decoded path contains dot segments or whose cleaned form differs, match and forward the same representation, and derive X-Forwarded-Prefix from the matched prefix.",
  close="A request for /api/../secret must be refused or routed by its cleaned path, never forwarded to the /api backend.",
  sources=[["backend/app/netproxy/https.go", 72, 80], ["backend/app/netproxy/https.go", 230, 247]]),

f(id="RR-07", origin="review", severity="Medium", area="Cluster networking", status="Source verified; kernel effect inferred",
  title="Duplicate WireGuard keys and underlay addresses are not rejected",
  cwe="CWE-345",
  impact="Nothing compares a node's reported WireGuard public key or underlay address with other nodes'. An enrolled node can report another node's key, both are published as distinct peers, and the kernel keys peers by public key, so the later configuration overwrites the earlier one and the victim's routes collapse onto the impostor.",
  trigger="A node that can complete enrollment, or any member node via its own report. Requires the victim's public key, which the netmap distributes to every node.",
  evidence="NormalizeNodeUnderlay checks address family only. ReportNode overwrites the node's addresses and key with whatever the mTLS-authenticated node sends. The publisher checks only that the key is non-empty.",
  validation="Source traced end to end; the peer-collapse step rests on WireGuard's documented per-key peer identity and was not executed in the VM harness.",
  fix="Enforce uniqueness of WireGuard public keys and underlay addresses at enrollment and on report, and bind the reported key to the node's enrollment identity.",
  close="A report carrying another node's public key must be rejected and logged.",
  sources=[["backend/app/primary/domain/nodes/nodes.go", 176, 191], ["backend/app/primary/netmappublisher/publisher.go", 290, 294]]),

f(id="RR-08", origin="review", severity="Medium", area="Agent sessions", status="Source verified",
  title="Agent-session approval never checks the approval code and request-start is unauthenticated",
  cwe="CWE-306 / CWE-451",
  impact="Anyone who can reach the web listener can open the single pending agent-session slot for any user by sequential id, and the request it plants looks identical to a legitimate one except for a code the API never verifies. Approving it hands the attacker a token with the approver's scopes. The slot can also be kept occupied to block the real agent.",
  trigger="Network reachability and a guessable user id (ids are dense from 1). For token theft, an operator approving without comparing the code.",
  evidence="AgentSessionApproveRequest carries only the id and the handler never reads ApprovalCode; the frontend renders the code read-only. request-start is NO_AUTH, keyed by user_id, and returns 409 while a pending request exists. The rate limit is 3 per 10 minutes per address.",
  validation="agent_sessions.go and the proto are unchanged since the audit; the design doc describes the code as an out-of-band human check.",
  fix="Require the approval code in the approve request and compare it server-side, bind request-start to a short-lived challenge issued to the operator, and allow more than one pending request per user so a planted request cannot block the real one.",
  close="Approving with a wrong or missing code must fail; a request planted by a third party must not be approvable by accident.",
  sources=[["backend/app/primary/webuihandler/agent_sessions.go", 160, 178], ["api-contract/model_sessions_operations.proto", 54, 56]]),

f(id="RR-09", origin="review", severity="Medium", area="Cluster PKI / availability", status="Source verified",
  title="The primary's cluster leaf certificate expires after one year and nothing reissues it",
  cwe="CWE-298",
  impact="Worker leaves renew from 90 days out, but the primary's own leaf is minted once at bootstrap with a 365-day validity and never renewed. Workers verify the full chain, so on day 366 every cluster connection fails and, because renewal rides that connection, nothing self-heals. A cluster-wide outage on a known date.",
  trigger="Time. The first affected installs reach the date one year after bootstrap.",
  evidence="GenerateNodeCertificateWithServerName sets NotAfter to now plus 365 days. BootstrapPrimary returns early whenever material exists, and run.go only loads it. The Web UI local TLS material has a self-renew path; the cluster leaf does not.",
  validation="Traced in source. No test advances the clock.",
  fix="Reissue the primary leaf under the existing CA before expiry using the same window logic as workers, and hot-swap the listener's certificate via GetCertificate.",
  close="A primary whose leaf is within the renewal window must present a fresh certificate without restart.",
  sources=[["backend/util/certu/sign.go", 126, 130], ["backend/app/primary/domain/pki/material.go", 138, 142]]),

f(id="RR-10", origin="review", severity="Medium", prior="Low", area="Authentication", status="Source verified",
  title="No re-authentication for passkey enrolment or master-password rotation, and the session token lives in localStorage",
  cwe="CWE-306 / CWE-922",
  impact="Any live human session token can add a new passkey and overwrite the master password without a fresh assertion or the old password, converting a stolen or XSS-read token into permanent account access and cluster-wide credential control. The token is stored in localStorage and echoed back by the current-session endpoint.",
  trigger="A session token obtained through XSS, a shared machine or a logged response. Bearer headers mean there is no CSRF exposure.",
  evidence="Passkey register endpoints require only requireHuman. PostV1AuthMasterPasswordSave requires human plus cluster update and never calls verifyMasterPassword; the separate verify endpoint is not bound to the save. login.js persists the token under authSession.",
  validation="auth.go, passkey.go and login.js are unchanged since the audit.",
  fix="Require a fresh passkey assertion or the current master password within the same request for enrolment and rotation, and stop echoing the token from the current-session endpoint.",
  close="Rotation without the current password, and enrolment without a recent assertion, must both fail.",
  sources=[["backend/app/primary/webuihandler/auth.go", 121, 139], ["backend/app/primary/webuihandler/access_enforce.go", 20, 28]]),

f(id="RR-11", origin="review", severity="High", prior="Medium", area="Ingress / ACME", status="Source verified",
  title="ACME certificates and private keys are shared by hostname across spaces",
  cwe="CWE-284 / CWE-522",
  impact="ACME issuance sweeps every deployment's HTTPS routes and stores certificates under a global hostname key. A node then receives the private key for any hostname its scheduled routes name. A deployment in space X that declares a route for space Y's hostname, on another node or under another path prefix, obtains Y's ACME private key and can serve Y's traffic.",
  trigger="Create or update permission in any space, and a hostname another space already uses. Claims collide only on the same node, address, port, hostname and prefix.",
  evidence="acmeHostnames iterates all configs with no space filter. addIngressCertRefs grants the secret by hostname match on the node's routes. The claim key includes node and prefix but no space, so different nodes or prefixes never conflict, and write-time validation inherits that.",
  validation="acmeissue and ingressplan are unchanged since the audit; the key-delivery path was traced across the two functions and not exercised end to end.",
  fix="Record the owning space for each hostname on first claim and reject routes from other spaces, key ACME secrets by space and hostname, and deliver a key only to routes of the owning space.",
  close="A second space claiming an existing hostname must be rejected at write time and must never receive the key.",
  sources=[["backend/app/primary/domain/acmeissue/acmeissue.go", 100, 118], ["backend/app/primary/clusterhandler/handler.go", 226, 244], ["backend/lib/ingressplan/ingressplan.go", 272, 277]]),

f(id="RR-12", origin="review", severity="Medium", area="Source resolver / SSRF", status="Source verified",
  title="The repository resolver accepts local paths, git@ SSH and any HTTP host",
  cwe="CWE-918 / CWE-200",
  impact="Anyone who can create a deployment gets outbound HTTP and SSH from the primary and a filesystem-existence oracle: an existing local path is passed to git and fails with \"not a git repository\", a missing one fails with a parse error, and a local git repository is cloned and built. The clone runs on the host as the agent user, on the primary during validation and on workers at prepare.",
  trigger="Create permission in any space, which agent sessions hold.",
  evidence="resolveCloneURL returns any existing path and any git@ prefix verbatim, and accepts http and https for any host; only option injection and non-http schemes are rejected. Deployment validation adds no host allowlist, and the repo-validate endpoint returns git's error text.",
  validation="Only a rename changed in this file since the audit.",
  fix="Accept only https URLs to an operator-configured host allowlist, reject local paths and git@ outright, and run validation fetches from a dedicated netns with the same egress policy as builds.",
  close="A repo of /etc/passwd or of an internal http host must be rejected before any process is started.",
  sources=[["backend/lib/repo/git/gitmanager.go", 67, 90]]),

f(id="RR-13", origin="review", severity="Medium", area="Spaces / authorization", status="Source verified",
  title="Space ids are reused after delete and nothing is cleaned up",
  cwe="CWE-694 / CWE-459",
  impact="The spaces table is a rowid alias without AUTOINCREMENT, so deleting the highest-numbered space and creating a new one reuses its id. Deletion removes only the row and node allow-lists; secrets, configs, assets, tombstoned deployments and every grant and rule template naming the id survive. Whoever holds a grant on the recycled id can reveal the previous tenant's secrets.",
  trigger="An operator deletes a space that still holds secrets or grants and later creates another.",
  evidence="schema_spaces.sql declares id INTEGER PRIMARY KEY with no AUTOINCREMENT. DeleteSpace issues a bare delete. The only precondition is no active deployments, and authorization reads the object's own space_id.",
  validation="Schema and DeleteSpace are byte-identical to the audited commit.",
  fix="Add AUTOINCREMENT or a monotonic counter, refuse deletion while secrets, configs, assets or grants reference the space, and tombstone grants and templates on delete.",
  close="Creating a space after deleting the last one must yield a new id, and the deleted space's grants must not apply to it.",
  sources=[["backend/storage/primarydb/pq/sql/schema_spaces.sql", 1, 4], ["backend/app/primary/domain/nodes/spaces.go", 125, 130]]),

f(id="RR-14", origin="review", severity="Medium", area="Backup / restore", status="Source verified",
  title="Backup restore accepts a tampered database",
  cwe="CWE-345",
  impact="Restore validates that the secrets store unlocks with the recovery code, that cluster TLS material decrypts and that the Web TLS bundle parses. Encrypted rows are authenticated individually, but users, grants, rule templates, the system config blob holding the master-password hash, and every deployment spec are plaintext. Anyone with bucket write access can add an admin or a hostile deployment to a genuine backup and it restores cleanly with the real CA.",
  trigger="Write access to the backup bucket and an operator performing a disaster-recovery restore.",
  evidence="Restore is stock litestream followed by unlockRestoredSecrets and primarybootstrap.Validate; no checksum, signature or manifest is checked. AEAD covers only secret ciphertext and keyslots.",
  validation="No verification code exists in backup/ or installer/restore.go; the tamper path is inferred from that absence and the schema, not executed.",
  fix="Sign or HMAC the backup manifest with a key derived from the recovery code, verify it before unlock, and print the restored admin list and deployment count for operator confirmation.",
  close="A backup with one plaintext row altered must fail restore.",
  sources=[["backend/app/installer/restore.go", 99, 116]]),

f(id="RR-15", origin="review", severity="Medium", area="Cross-space authorship", status="Source verified",
  title="Secret and config rotation writes deployments across spaces, and delete refusals leak cross-space names",
  cwe="CWE-863 / CWE-200",
  impact="Rotating a shared secret enumerates every deployment in the cluster and writes a new event for each referencing one, authored as the caller and bypassing the deployment update path and its host-access gate. The caller needs only update on the secret. Separately, the delete refusal for a referenced secret, config or asset lists referencing deployments as space / node / name cluster-wide, with no visibility filter.",
  trigger="Update on a secret that deployments in other spaces reference (any space may reference default-space secrets), or delete on any referenced value.",
  evidence="references.go lists latest deployment events without a space filter and inserts events directly. RefDetails renders every referencing deployment's space, node and name into the client-visible message, and panics on a query error inside the write transaction.",
  validation="Both files are unchanged since the audit. Count mismatches surface only as a fixed 409, so the version oracle is one bit per attempt.",
  fix="Restrict rotation to deployments the caller can update and report the rest as skipped, route the writes through the deployment service, and filter refusal details to deployments the caller can see.",
  close="A caller without update in space Y must not author events there, and must not see Y's deployment names in a refusal.",
  sources=[["backend/app/primary/domain/values/references.go", 87, 91], ["backend/app/primary/domain/deployments/refs.go", 117, 147]]),

f(id="RR-16", origin="review", severity="Low", area="Filesystem hygiene", status="Source verified",
  title="Asset cache, volume and store roots are world-readable and the mode is re-applied",
  cwe="CWE-732",
  impact="The asset cache, volumes root, per-deployment volume directories, releases and the new Nix store root are 0755 and asset files 0644, so any local user on the node reads asset plaintext, asset-backed env values and volume data. The mode is re-applied at every agent start, install and prepare, so tightening it by hand is silently undone.",
  trigger="An unprivileged local shell on a node.",
  evidence="ainit ensures the roots at 0755 on every start via MkdirAll plus an unconditional Chmod; the runner creates volume directories at 0755 and only chowns the leaf.",
  validation="Unchanged apart from the added store root.",
  fix="Create roots at 0750 owned by the opendeploy group, add the workload uid to that group or chown per directory, and stop re-applying modes to existing directories.",
  close="A user outside the opendeploy group must be unable to list the asset cache or a volume directory.",
  sources=[["backend/ainit/ainit.go", 68, 76], ["backend/lib/engine/runner/container.go", 1124, 1129]]),

f(id="RR-17", origin="review", severity="Low", area="Installer", status="Source verified",
  title="Restore credentials and the recovery code are passed on argv",
  cwe="CWE-214",
  impact="The install command takes the S3 secret access key and the secrets-store recovery code as flags, so both are visible in the process list and land in shell history for the duration of a restore. The litestream child already receives its configuration over stdin; the CLI hop is the gap.",
  trigger="A local user during the install window, or later access to shell history.",
  evidence="installer.go declares --restore-s3-secret-access-key and --recovery-code, and restore validation requires both with no env or file alternative.",
  validation="Unchanged since the audit.",
  fix="Read both from environment variables or a file descriptor, and print a warning if they arrive on argv.",
  close="A restore must be possible without either secret appearing in argv.",
  sources=[["backend/app/installer/installer.go", 108, 114]]),

f(id="RR-18", origin="review", severity="Low", area="Audit trail", status="Source verified",
  title="Secret reveals are not audited and the generated audit hook is unused",
  cwe="CWE-778",
  impact="Reveal authorises, decrypts and returns with no log line and no row. Writes are event-sourced with an author, but reads leave no trace, so a compromised session can drain a space's secrets undetected. The mux exposes an Audit callback that nothing wires up.",
  trigger="Any principal with reveal.",
  evidence="PostV1SecretsReveal and RevealByID contain no logging; MuxConfig.Audit is defined and never set in run.go. Cluster settings changes and user creation likewise lack an actor record.",
  validation="Unchanged since the audit.",
  fix="Append a reveal event with actor, secret id and source address to a retained audit table and wire the mux Audit hook for privileged verbs.",
  close="Every reveal must be attributable to a session after the fact.",
  sources=[["backend/app/primary/webuihandler/secrets.go", 292, 308], ["backend/apigen/mux.gen.go", 20, 24]]),

f(id="RR-19", origin="review", severity="Medium", area="Log storage / availability", status="Source verified",
  title="There is no per-node log volume cap",
  cwe="CWE-770",
  impact="Retention is by age only (30 days) and ingestion has no rate limit or backpressure; drops happen only after a write already failed. A deployment logging more than the disk can hold in 30 days fills the volume, at which point compaction stops because the free-space check lives inside it. The log archive shares /var/lib with the primary database and containerd on a default install.",
  trigger="One chatty or looping workload.",
  evidence="retentionDays is the only pruning parameter; rollupTargetBytes is an output size target. ensureFreeSpace is called from compaction only.",
  validation="lib/log is unchanged since the audit.",
  fix="Add a per-node byte budget that evicts oldest files first, a per-deployment ingestion rate cap with drop accounting, and run the free-space check on the write path.",
  close="A workload writing at line rate must not consume more than its budget, and the agent must keep running when the log volume is full.",
  sources=[["backend/lib/log/logmanager/maintenance.go", 17, 24]]),

# ---------------------------------------------------------------- new in this re-review
f(id="RR-20", origin="new", severity="Medium", area="DNS / workload PKI", status="Source verified",
  title="Deployment names that collide under DNSLabel merge DNS endpoints and yield same-name certificates",
  cwe="CWE-706",
  impact="Names are unique on the raw string per node and space, but the DNS identity is the lossy DNSLabel: Api, api_x and api-x, or the same name on two nodes, collapse onto one label. Colliding services are merged rather than rejected, so a second deployment receives a share of the victim's traffic and, because its own DNS name is byte-identical, a legitimate issued-TLS certificate for the victim's exact name with no extra names at all.",
  trigger="Create permission in the victim's space.",
  evidence="Name validation requires non-emptiness only and uniqueness includes the node. DNSLabel lower-cases, maps underscores and drops other characters. The netproxy catalog appends endpoints to an existing service of the same label, and the DNS server answers with all of them.",
  validation="Traced in source; not exercised.",
  fix="Enforce uniqueness of DNSLabel(name) per space across nodes at write time, and reject names that change under DNSLabel.",
  close="Creating api_x in a space that already has api-x must fail.",
  sources=[["backend/lib/network/names.go", 8, 30], ["backend/app/netproxy/netstate.go", 188, 196]]),

f(id="RR-21", origin="new", severity="Medium", area="Authorization / upgrade", status="Source verified; conditional",
  title="Operator-authored wildcard rule templates silently gained the host verbs",
  cwe="CWE-276",
  impact="A wildcard permission selector matches any verb after exclusions, and the startup upgrade that excludes use_host_mounts and use_host_network rewrites only the two built-in templates. Any template an operator created before the change with a wildcard selector now grants both host verbs to its holders without anyone having decided that.",
  trigger="An existing cluster with at least one custom wildcard template. Whether any such template exists must be checked on each live cluster.",
  evidence="selectorMatches returns true for Wildcard after the exclude list; the upgrade path touches template ids 1 and 2 only.",
  validation="Inference from the evaluator and the upgrade code; no live cluster was inspected.",
  fix="Add the two verbs to the exclude list of every persisted wildcard selector during the upgrade, and require new dangerous verbs to be opted into explicitly rather than matched by wildcard.",
  close="After upgrade, no template other than cluster_admin may match use_host_network.",
  sources=[["backend/app/primary/domain/authz/evaluate.go", 14, 29]]),

f(id="RR-22", origin="new", severity="Medium", area="Build network", status="Source verified",
  title="Build containers can reach the node's private network",
  cwe="CWE-284",
  impact="The documented build policy is public egress only. The IPv4 deny list is loopback and link-local, plus the cluster prefix and peer underlays; RFC1918 and other LAN ranges fall through to the forward chain's accept. A hostile flake's build, which any deployment author can trigger, reaches databases, storage and management interfaces on the node's private network.",
  trigger="Create or update permission on a Nix deployment and a private network reachable from the node.",
  evidence="buildDenied4 lists 169.254.0.0/16 and 127.0.0.0/8 only; buildEgressRules adds the cluster prefix and underlays and then accepts. Build attachments are masqueraded out the default interface.",
  validation="Traced in source; the design document's stated policy is stricter than the rules.",
  fix="Deny 10/8, 172.16/12, 192.168/16, non-cluster ULA and the node's own subnets by default, with an operator allow-list for legitimate private build inputs.",
  close="A build must be unable to connect to an RFC1918 address that is not explicitly allowed.",
  sources=[["backend/lib/network/filter.go", 39, 42]]),

f(id="RR-23", origin="new", severity="Low", area="Enrollment / availability", status="Source verified",
  title="Unauthenticated enrollment creates permanent node rows with no rate limit",
  cwe="CWE-770",
  impact="A first hello inserts a node row and ending the request only flips its status; nothing deletes it. The enrollment listener has no client authentication and no rate limit, so anyone reaching :9444 can mint keys and grow the node table and event log without bound. The CSR requirement adds one keygen per row.",
  trigger="Network reachability of the enrollment port.",
  evidence="UpsertEnrollmentRequest inserts on first sight; EndEnrollmentRequest sets ENROLLMENT_CANCELLED or EXPIRED and keeps the row.",
  validation="Traced in source.",
  fix="Rate-limit hellos per address, cap the number of pending enrollments, and garbage-collect cancelled and expired rows after a grace period.",
  close="A hello flood must be throttled and must not leave permanent rows.",
  sources=[["backend/app/primary/domain/nodes/enrollment.go", 170, 195]]),

f(id="RR-24", origin="new", severity="Medium", area="PKI lifecycle", status="Source verified",
  title="No revocation exists for cluster or workload certificates: 100-year CAs and 10-year leaves",
  cwe="CWE-299",
  impact="Both CAs are valid for a century and workload leaves for a decade, with no CRL, OCSP or serial denylist anywhere. OD-04 and OD-07 are now closed at the application layer, which is where containment lives: once a leaf under either CA is issued or stolen it stays cryptographically valid, and the only certificate-level remedy is re-bootstrapping the CA, which invalidates every node and workload certificate at once.",
  trigger="Any leaf compromise or mis-issuance.",
  evidence="caValidity is 100 years for both CAs, issuedtls.Validity is 10 years, and KeyUsageCRLSign is set on the CA templates but never used.",
  validation="Traced in source.",
  fix="Shorten workload leaves to days with automatic reissue on rollover, introduce a serial denylist distributed with the netmap, and plan CA rotation with an overlap window.",
  close="Revoking one workload leaf must not require touching the CA.",
  sources=[["backend/util/certu/sign.go", 55, 58], ["backend/lib/issuedtls/issuedtls.go", 10, 10]]),

f(id="RR-25", origin="new", severity="Low", area="Secret rotation", status="Source verified",
  title="Secret rotation misses ingress TLS certificate references",
  cwe="CWE-672",
  impact="Rotation scans only container env vars, while delete-blocking and the runtime input set also cover ingress HTTPS cert sources. Rotating a secret used as an ingress certificate leaves the route pinned to the old version silently, so a revoked key stays live.",
  trigger="Normal rotation of a certificate secret.",
  evidence="deploymentUsesReferences and replaceDeploymentReferences iterate Runtime.EnvVars only; runtimeinputs.SecretRefs includes CertSource.Secret.",
  validation="Traced in source.",
  fix="Use runtimeinputs.SecretRefs for both the match and the rewrite.",
  close="Rotating an ingress certificate secret must update the route's secret version.",
  sources=[["backend/app/primary/domain/values/references.go", 147, 157]]),

f(id="RR-26", origin="new", severity="Low", area="Build ingest", status="Source verified; inference",
  title="The nix2container tar rewrite relies on containerd's unpack for path safety",
  cwe="CWE-22",
  impact="The layer tar builder applies a flake-controlled regex rewrite to entry destinations and splits on slashes without rejecting dot segments. The bytes become a layer blob that containerd 2.3.5 unpacks, so traversal safety rests on containerd's extraction protections rather than on this code.",
  trigger="A hostile flake with a rewrite rule, and a containerd unpack regression.",
  evidence="tarPath and addFileToGraph honour PathOptions.Rewrite on the destination name with no sanitisation.",
  validation="Inference from reading tar.go; no malicious layer was built.",
  fix="Reject rewritten destinations containing dot segments or absolute paths before writing the tar, and add a test that a hostile rewrite cannot produce an escaping entry.",
  close="A rewrite producing ../ must fail the build.",
  sources=[["backend/lib/engine/prepare/nix2container/tar.go", 69, 90]]),
]


CLOSED = [
  dict(id="OD-07", prior="High", title="A stolen worker key can renew its access without an effective revocation boundary",
       how="Closed after the review by 60d73bc (v0.0.609) on 15 September. Nodes can now be evicted: the node moves to EVICTED in one commit, its placements are finalized, its system deployments are deleted and the scheduler never places on it again. Every cluster endpoint, including certificate renewal, resolves the client certificate's CN against member statuses only, so an evicted node's still-valid certificate is refused with 403, a reconnect is refused with node_evicted (410) and a live session receives a final evicted frame. The identifier is refused at enrollment forever; a reinstall is the only way back. Renewal still re-signs the presented key and there is no PKI-level serial denylist: on 15 September the project recorded the member-status check at the handler layer as the accepted revocation boundary for cluster certificates. The certificate-lifetime half of the problem remains open as RR-24. Unit tests cover the refusal without force, forced finalization, permanent re-enrollment refusal, the 403 on cluster endpoints and the session frame.",
       commit="60d73bcb152cf52a4650b394d9ff505dc71f63cc",
       sources=[["backend/app/primary/domain/nodes/evict.go", 81], ["backend/app/primary/clusterhandler/handler.go", 413], ["backend/app/primary/clusterhandler/handler.go", 439], ["backend/app/primary/domain/nodes/enrollment.go", 33], ["backend/app/secondary/eviction.go", 32]]),
  dict(id="OD-04", prior="High", title="Workload certificates can carry another space's names",
       how="Closed after the review by 23ae086 on 13 September. Extra names are now checked against the deployment's space at create, update and space move, and again in the issuer: a .internal name must lie in the deployment's own space-<id>.internal zone and a cluster-prefix address must be the deployment's own; external names are untouched. The audit's issuer reproduction no longer passes. Its deployments reproduction still passes because it calls the syntax-only mount validator, which has no space context; the corrected check through the create handler passes (od-04-closure-results.txt). Unit tests cover the helper, the validator, the issuer and the handler.",
       commit="23ae0864b0e350f8ae91a50c25dd149464c6444e",
       sources=[["backend/lib/network/names.go", 42], ["backend/app/primary/domain/deployments/validate.go", 757], ["backend/app/primary/domain/deployments/validate_layers.go", 128], ["backend/app/primary/domain/pki/issuer.go", 49]]),
  dict(id="OD-01", prior="Critical", title="Nix image exporters execute with control-plane privileges",
       how="b6ce56b moved Nix builds into a containerd container: default seccomp and capabilities, egress-only network, per-repository store, memory/CPU/pids limits, no agent environment. The host exec of the flake output is gone; layers are digest-verified on ingest.",
       sources=[["backend/lib/engine/prepare/nixstore/build.go", 69], ["backend/lib/engine/prepare/nix2container/ingest.go", 153]]),
  dict(id="OD-02 (part)", prior="Critical", title="Host-mount denylist bypassed with parent paths; space_admin could mount host paths",
       how="The denylist now rejects parents of protected roots and /. Custom host mounts require use_host_mounts, held by cluster_admin only and excluded from agents. The audit's two mount reproductions no longer pass. The lexical symlink case remains open as OD-02 at Low.",
       sources=[["backend/app/primary/domain/deployments/validate.go", 910], ["backend/app/primary/domain/authz/builtin.go", 108], ["backend/app/primary/webuihandler/deployments.go", 44]]),
  dict(id="OD-03", prior="High", title="The bundled container runtime has published escape and DoS vulnerabilities",
       how="Pinned containerd 2.3.5 and runc 1.5.1, the newest stable releases on the review date; the agent reconciles installed binaries against the pin at every start and never downgrades. The September containerd advisories are fixed in 2.3.5 or confined to the CRI plugin, which is not used.",
       sources=[["backend/lib/runtimebin/runtimebin.go", 46], ["backend/lib/runtimebin/runtimebin.go", 60]]),
  dict(id="OD-08", prior="High", title="Netproxy uses an HTTP/2 transport with a known infinite-loop vulnerability",
       how="golang.org/x/net moved from v0.52.0 to v0.55.0; GO-2026-4918 was fixed in v0.53.0 and no longer appears in govulncheck for either target.",
       sources=[["backend/go.mod", 28]]),
  dict(id="12 Sep", prior="High", title="Unauthenticated enrollment hello rewrites a member node's identity",
       how="Hellos for member identifiers are rejected inside the commit transaction, and the identifier is now the SHA-256 of the CSR's public key, verified on every hello, so only the key holder can act on an identifier. X-Forwarded-For is no longer trusted.",
       sources=[["backend/app/primary/domain/nodes/enrollment.go", 30], ["backend/util/certu/sign.go", 395]]),
  dict(id="12 Sep", prior="High", title="Host networking has no admin gate",
       how="use_host_network is enforced on create, v2 update (including the saved spec) and space moves; only cluster_admin holds it. The policy-bypass half stays open as RR-04 at Medium.",
       sources=[["backend/app/primary/webuihandler/deployments.go", 100], ["backend/app/primary/domain/authz/builtin.go", 30]]),
  dict(id="12 Sep", prior="Low", title="The Nix build and exporter inherit the full service environment",
       how="The build container's environment is constructed explicitly (TMPDIR, HOME and the CA bundle variables); no host environment, token or credential is forwarded, and the checkout is read-only with .git masked.",
       sources=[["backend/lib/engine/prepare/nixstore/build.go", 88]]),
]

ACCEPTED = [
  dict(id="OD-05", prior="High", title="Agent secret restrictions can be bypassed through controlled workloads",
       position="AP-1 / AP-2",
       why="The behaviour is present and reproduced (corrected test): a delegated session denied reveal still binds a secret into a workload it controls. The project records this as documented authority: secret : reveal bounds the direct path only, and deployment create or update in a space is a second, intended path to every secret bindable from that space. The builtin agent defaults withhold reveal and view_logs as a guard rail against accidental disclosure, not as an isolation boundary. Operators who need a value kept from an agent must scope its deployment permissions away from the secret's space. Under security-severity.md this is Informational. The positions were written into docs/engineering/auth.md on 13 September, after the reviewed commit, so the pinned source links below show the code paths only.",
       sources=[["backend/app/primary/domain/deployments/validate_refs.go", 34], ["backend/app/primary/domain/authz/builtin.go", 37]]),
]


# ================================================================ 22 November 2026 re-check
# Applied on top of the lists above by _apply_recheck() at the end of the file.
RECHECK_COMMIT = "ef1f3508acdb7478f13fa695e7f02a3de8e4046f"
RECHECK_CLOSED_IDS = {"RR-13"}

RECHECK_UPDATES = {
"OD-02": dict(
  recheck="Still open. The gate and the denylist hold: a ported check shows /, /var, /var/lib and /var/lib/opendeploy rejected and a space_admin denied a custom mount. The same check shows a symlink that resolves to /var/lib/opendeploy passing the validator.",
  evidence="containerHostMountDenied cleans the path and compares it with the denylist in both directions; its comment states the check is lexical and that administrators own symlinks. The runner copies HostPath into the mount unchanged and the containerd client only stats it to choose bind or rbind; no EvalSymlinks, openat2 or RESOLVE_BENEATH touches a mount source. The per-repository Nix store root, /var/lib/opendeploy-nix, is not on the denylist, and the list is hard-coded to /var/lib/opendeploy* whatever data directory is configured.",
  validation="Reproduced at the validator on 22 November (TestRecheckMountParentRejectedSymlinkAccepted, TestRecheckSpaceAdminHostMountDenied). No mount was executed.",
  close="A symlink from an allowed host path into /var/lib/opendeploy must fail the run with a clear error; ordinary host mounts must keep working; the Nix store root must be on the denylist.",
  sources=[["backend/app/primary/domain/deployments/validate.go", 883, 897], ["backend/lib/engine/runner/container.go", 1507, 1513], ["backend/lib/engine/ctrd/client.go", 254, 271]]),

"OD-06": dict(
  recheck="Still open. The handler was ported to the new contract with the same body: one coarse usesGithub check, then the whole token.",
  evidence="GetV1ClusterGithubCredentials returns the raw token after a coarse usesGithub check, which any scheduled Nix image build or ghcr.io remote image satisfies. The worker caches it in memory. The git manager injects it as a github.com-scoped extraHeader so it never reaches .git/config or argv, and the build container's env is TMPDIR, HOME and the CA bundle only.",
  validation="Read at ef1f3508. The containerized build and its egress rules are unchanged since 16 September.",
  sources=[["backend/app/primary/clusterhandler/handler.go", 154, 167], ["backend/app/primary/clusterhandler/handler.go", 269, 281], ["backend/lib/engine/prepare/nixstore/build.go", 89, 93]]),

"OD-09": dict(
  recheck="Still open, and the default exposure is wider on the unreleased branch: per-user password login is the default sign-in there, so two unauthenticated routes hash on demand instead of one.",
  impact="Each master-password or password-login attempt costs a 64 MiB Argon2id hash. Per-address rate limits exist and key on the socket address, but nothing bounds aggregate concurrency, and the limiter keys on the exact address, so an IPv6 /64 supplies unlimited independent buckets. Each of the two routes allows a burst of ten per address. require_passkey turns password login off before hashing; the master exchange always hashes.",
  evidence="The middleware chain applies PerIP and PerIPAndPrefix limits; ratelimit.Global exists but is unused, and no semaphore wraps VerifyPassword. The limiter allocates one bucket per distinct address string. PostV1AuthMaster is NO_AUTH and hashes before any other check; PostV1AuthPasswordLogin hashes a decoy for unknown users.",
  validation="Read at ef1f3508. Load impact was not measured. RR-31 covers the authenticated hashing route that sits outside these limits.",
  sources=[["backend/app/primary/run.go", 129, 145], ["backend/lib/middleware/ratelimit/ratelimit.go", 67, 83], ["backend/app/primary/webuihandler/auth.go", 44, 67]]),

"OD-10": dict(
  recheck="Still open on all four listeners. The Web UI server was rewritten into a manager with swappable listeners and still sets no timeout; the cluster and enrollment servers are byte-identical to 16 September. RR-28 raises the stakes: the release toolchain carries unauthenticated HTTP/2 and TLS denial-of-service advisories on the same listeners.",
  evidence="The cluster server literal carries Handler, TLSConfig and BaseContext plus HTTP/2 ping timeouts, which act after the handshake; the enrollment literal has no timeouts; the Web UI literal carries Handler and BaseContext with TLSConfig attached afterwards, and also serves the plain-HTTP listener. The stdlib derives the handshake deadline from the minimum positive server timeout, so three zeros yield none. Netproxy's HTTPS listener has a 10-second header timeout, but its ClientHello deadline is cleared before hand-off onto an unbuffered channel, leaving an unbounded queueing window.",
  validation="Read at ef1f3508; no runtime probe was run.",
  sources=[["backend/app/primary/clusterserver/server.go", 42, 52], ["backend/app/primary/clusterserver/server.go", 84, 89], ["backend/app/primary/webui/server.go", 266, 286]]),

"OD-11": dict(
  recheck="Still open. Both loopback reproductions were rerun unchanged on 22 November and pass; the three packages have an empty diff since 16 September.",
  validation="Both audit reproductions pass against ef1f3508 on loopback. The repo-validate route returns a generic message for images, so that entry point is a blind request; the deployment versions route returns the error text.",
  sources=[["backend/lib/engine/versionprovider/containerimage.go", 196, 220], ["backend/lib/engine/versionprovider/containerimage.go", 155, 164], ["backend/lib/engine/versionprovider/containerimage.go", 166, 181]]),

"OD-12": dict(
  severity="Low", prior="Medium", status="Source verified; residual",
  title="Passkey clone warnings are recorded but never acted on",
  recheck="Narrowed. The duplicate-row defect closed in v0.0.614 (80b8fbd): credentials are replaced in place by id, and a ported check through the service callback leaves one row carrying the latest counter. What remains is the second half of the original fix.",
  impact="When an authenticator's signature counter fails to advance, the WebAuthn library sets CloneWarning on the credential and still accepts the login. Nothing in the backend reads the flag, so a cloned counter-based authenticator signs in without a refusal, a log line or an alert. Synced passkeys report a zero counter and are unaffected either way.",
  trigger="A cloned authenticator that maintains a signature counter.",
  evidence="The shared save callback now calls users.SetCredential, which replaces the entry with the same credential id and stamps it as used. The stored JSON therefore carries the latest counter and the library's cloneWarning field, but CloneWarning is not referenced anywhere under backend/.",
  validation="Closure of the duplicate-row half executed on 22 November (TestRecheckPasskeySaveReplacesByCredentialID); the clone-warning half was traced in source and in go-webauthn v0.16.3.",
  fix="After FinishLogin, read CloneWarning on the returned credential and refuse the login or raise an operator-visible alert.",
  close="A login whose signature counter does not advance must be refused or must raise an alert an administrator sees.",
  sources=[["backend/app/primary/webuihandler/passkey.go", 60, 70], ["backend/app/primary/domain/users/users.go", 174, 188]]),

"OD-13": dict(
  recheck="Still open and larger: 20 advisories on 22 November against 14 in September, with a byte-identical lockfile. The new entries are fresh advisories against the same pinned packages.",
  impact="Twenty advisories (9 high, 10 moderate, 1 low) are reported, all reached through vite, postcss, nanoid, source-map-js and yaml. None ships in the bundle served to browsers; the exposure is a developer running the dev server. pnpm audit --prod reports the same set because the tailwind vite plugin sits in dependencies.",
  evidence="pnpm-lock.yaml is unchanged since 13 August. vite is pinned at 7.1.7 in devDependencies; tailwindcss and @tailwindcss/vite are in dependencies. The runtime dependency set is codemirror, lezer, bip39, vanjs and vanjs-feather, none of which carries an advisory.",
  validation="pnpm audit and pnpm audit --prod were rerun read-only on 22 November; the inventory is preserved in the evidence directory.",
  fix="Bump vite to the current 7.x, which pulls fixed postcss and nanoid, and move tailwindcss and @tailwindcss/vite to devDependencies so the production audit is meaningful.",
  sources=[["frontend/package.json", 17, 37]]),

"RR-01": dict(
  title="Workload containers have no CPU, memory or pids limits, and the /dev/shm override has no upper bound",
  recheck="Still open. The runner was reworked for the new data model and the workload spec still never sets Resources; the deployment contract has no resource fields.",
  evidence="The workload ContainerSpec sets ID, image, user, env, args, cwd, dev-shm size, file-descriptor limit, mounts, log fields and netns, and never populates Resources, so the OCI memory, CPU and pids options are skipped. dev_shm_size_kb is an optional value with a lower bound of 1 and no upper bound; unset, the runtime default of 64 MiB applies. OOM detection is wired to builds only.",
  validation="Read at ef1f3508. The resource wiring added for builds applies only when Resources is non-nil.",
  sources=[["backend/lib/engine/runner/container.go", 550, 568], ["backend/lib/engine/ctrd/client.go", 323, 334], ["backend/app/primary/domain/deployments/validate.go", 769, 777]]),

"RR-02": dict(
  recheck="Still open; the containerd client code is unchanged since 16 September.",
  validation="Read at ef1f3508. CAP_NET_RAW was confirmed in the default capability list of the pinned containerd v2.3.5 module, not on a running container.",
  sources=[["backend/lib/engine/ctrd/client.go", 335, 337], ["backend/lib/engine/runner/container.go", 550, 568], ["backend/lib/engine/ctrd/client.go", 302, 304]]),

"RR-03": dict(
  recheck="Still open. An existing test (TestDeploymentHostAccessDefaultsAndManagedVolumes) asserts the behaviour: a read-write cross mount succeeds without any host permission.",
  validation="Read at ef1f3508. The exempted space is the global space (1); the system space is 0. The other refusals in the same function act as existence, node and volume oracles by deployment id.",
  sources=[["backend/app/primary/domain/deployments/validate.go", 899, 923], ["backend/app/primary/webuihandler/deployments.go", 99, 111], ["backend/lib/engine/runner/container.go", 1500, 1506]]),

"RR-04": dict(
  recheck="Still open. The permission gate was ported and holds; the isolation gap is identical.",
  evidence="The gate is enforced on create, on update against both the saved and the proposed spec, and on space moves, and agent rules exclude the verb. The input chain matches only the build interface set and its policy is accept; host-mode containers have no veth to match on. The containerd client sets no capability option, so containerd's default set applies.",
  validation="Gate tests pass at ef1f3508. Policy behaviour was traced in source; no Linux enforcement test was run.",
  sources=[["backend/app/primary/webuihandler/deployments.go", 99, 121], ["backend/lib/network/filter.go", 141, 155]]),

"RR-05": dict(
  recheck="Still open. The ingress plan was rewritten around per-node, per-port selector sets and forward handling is equivalent: only a forward against an ingress claim is rejected.",
  evidence="validatePortForwarding checks protocol, range and duplicates within one spec. Reservations and the TCP port map are consulted only inside the ingress-claim loop; the map records the first TCP owner without a diagnostic and UDP forwards are dropped from the plan input. HostPortRule for a forward carries no Dest, so the DNAT rule falls back to fib daddr type local in both prerouting and output.",
  validation="Read at ef1f3508; no VM test was run. The only test, TestTCPPortForwardConflictsWithIngress, covers forward against ingress.",
  sources=[["backend/app/primary/domain/deployments/validate.go", 171, 196], ["backend/lib/ingressplan/ingressplan.go", 255, 270], ["backend/lib/engine/runner/container.go", 1366, 1388]]),

"RR-06": dict(
  recheck="Still open; the netproxy diff since 16 September is type-level only.",
  validation="Read at ef1f3508. No fuzzing of encoded or dot-segment paths was run.",
  sources=[["backend/app/netproxy/https.go", 73, 80], ["backend/app/netproxy/https.go", 172, 188], ["backend/app/netproxy/https.go", 230, 246]]),

"RR-07": dict(
  recheck="Still open, with a narrower prerequisite. The publisher that distributed every key to every node was replaced on 8 October; maps are now scoped per node. The render-time checks that the old publisher had were removed and no write-time uniqueness check replaced them.",
  impact="Nothing compares a node's reported WireGuard public key or underlay address with other nodes'. An enrolled node can report another node's key; both are published as distinct peers to every node whose map holds both, and the kernel keys peers by public key, so the later configuration overwrites the earlier one and the victim's routes collapse onto the impostor.",
  trigger="A member node, via its own report. It needs the victim's public key, which it receives only when the victim hosts a placement in one of the spaces its own map covers.",
  evidence="NormalizeNodeUnderlay checks parse, zone and address family only. ReportNode writes the reported underlay and key onto the node row with no comparison to other nodes, and the node table has no uniqueness constraint on either column. The node publisher copies the reported key into each projection unchecked.",
  validation="Source traced end to end at ef1f3508; the peer-collapse step rests on WireGuard's documented per-key peer identity and was not executed in the VM harness.",
  sources=[["backend/app/primary/domain/nodes/nodes.go", 186, 203], ["backend/app/primary/domain/nodes/nodes.go", 204, 225], ["backend/app/primary/nodepublisher/render.go", 72, 79]]),

"RR-08": dict(
  recheck="Still open. The handlers and contract were rewritten with the same behaviour; existing tests pin the single pending slot.",
  impact="Anyone who can reach the web listener can open the single pending agent-session slot for any user by sequential id, and the request it plants looks identical to a legitimate one except for a code the API never verifies. Approving it hands the attacker a delegated token that acts as the approver within the delegable rules. The slot can also be kept occupied to block the real agent.",
  trigger="Network reachability and a guessable user id (ids are dense from 1, and an unknown id answers with a distinct 404). For token theft, an operator approving without comparing the code.",
  evidence="AgentSessionApproveRequest carries only session_id and the handler never reads ApprovalCode; the frontend renders the code read-only. request-start is NO_AUTH, keyed by user_id, and returns 409 while a pending request younger than ten minutes exists. The limiter allows a burst of three and then one request per twenty seconds per address; one request per ten minutes is enough to hold a user's slot.",
  validation="Read at ef1f3508. The earlier wording of the rate limit (3 per 10 minutes) was wrong and is corrected here; auth.md repeats the error.",
  sources=[["backend/app/primary/webuihandler/agent_sessions.go", 241, 262], ["backend/app/primary/webuihandler/agent_sessions.go", 116, 137], ["api-contract/model_sessions_operations.proto", 59, 61]]),

"RR-09": dict(
  recheck="Still open; nothing reissues the leaf. A fix should keep the key, because the enrollment pin is derived from this certificate.",
  validation="Traced in source at ef1f3508. No test advances the clock.",
  sources=[["backend/util/certu/sign.go", 126, 136], ["backend/app/primary/domain/pki/material.go", 138, 154]]),

"RR-10": dict(
  recheck="Still open; both closure conditions fail. One new control exists on the unreleased branch: changing or clearing an existing account password requires the current one. Setting a first password and deleting a passkey do not.",
  impact="Any live human session token can add a passkey to the account and overwrite the master password without a fresh assertion or the old password. The master password no longer signs anyone in, but it opens a root session that can create users with any grants and open a session for any user, so the consequence is unchanged. The token is stored in localStorage and echoed back by the current-session endpoint.",
  evidence="Passkey register endpoints require only requireHuman. PostV1AuthMasterPasswordSave requires human plus cluster update and never calls verifyMasterPassword; the separate verify endpoint is not bound to the save. login.js persists the token under authSession. Sessions are now server-side rows the owner can list and revoke, but revoking a session does not remove a passkey added through it, and signing out clears local storage without revoking the session.",
  validation="Read at ef1f3508. RR-31 covers the password-change route, which also acts as an unthrottled oracle for the current password.",
  sources=[["backend/app/primary/webuihandler/auth.go", 69, 98], ["backend/app/primary/webuihandler/passkey.go", 180, 204], ["frontend/src/state/login.js", 104, 115]]),

"RR-11": dict(
  recheck="Still open. Claim resolution was rewritten and still has no notion of space. One addition: the bundle is an ordinary global-space secret whose name is not reserved.",
  evidence="acmeHostnames sweeps every deployment's HTTPS routes with no space filter. The bundle is stored as the global-space secret acme.cert.<hostname>; that name is not reserved, so a deployment in any space can also bind it as an ordinary secret. addIngressCertRefs grants the secret to a node by hostname match on its scheduled routes. resolveClaim compares hostname, kind and prefix on the same node and port with intersecting listen sets and has no space field, so another node or another prefix never conflicts.",
  validation="Read at ef1f3508; the key-delivery path was traced across the functions and not exercised end to end.",
  sources=[["backend/app/primary/domain/acmeissue/acmeissue.go", 105, 126], ["backend/app/primary/clusterhandler/handler.go", 232, 250], ["backend/lib/ingressplan/ingressplan.go", 344, 361]]),

"RR-12": dict(
  recheck="Still open; backend/lib/repo has an empty diff since 16 September.",
  validation="Read at ef1f3508.",
  sources=[["backend/lib/repo/git/gitmanager.go", 67, 90], ["backend/app/primary/webuihandler/repo_validate.go", 60, 70]]),

"RR-14": dict(
  recheck="Still open. The data-model rewrite widened what is plaintext, and a change to how secrets are sealed removed one thing that used to be authenticated.",
  impact="Restore validates that the secrets store unlocks with the recovery code, that cluster TLS material decrypts and that the Web TLS bundle parses. Encrypted rows are authenticated individually, but users with their password hashes, grants, rule templates, the system config holding the master-password hash, every deployment spec, and the write log and tables themselves are plaintext. Anyone with bucket write access can add an admin or a hostile deployment to a genuine backup and it restores cleanly with the real CA.",
  evidence="Restore is stock litestream followed by unlockRestoredSecrets and primarybootstrap.Validate; no checksum, signature or manifest is checked, and nothing reconciles the tables against the write log at restore or start. Secret seals now bind the secret id only: the associated data no longer carries the system class or name, so relabelling a row's space or name is not detected. A system secret relabelled into the global space becomes revealable, and a known plaintext relabelled as the cluster CA would be loaded at the next start.",
  validation="No verification code exists in backup/ or installer/restore.go; the tamper paths are inferred from that absence, the schema and the seal's associated data, not executed.",
  fix="Sign or HMAC the backup manifest with a key derived from the recovery code, verify it before unlock, print the restored admin list and deployment count for operator confirmation, and put the secret's class and system name back into the seal's associated data.",
  sources=[["backend/app/installer/restore.go", 99, 116], ["backend/app/primary/backup/process.go", 161, 182], ["backend/app/primary/domain/secrets/secrets.go", 673, 678]]),

"RR-15": dict(
  recheck="Still open in both halves. Rotation now appends deployment mutations to the same commit instead of inserting events, with the same reach; existing tests assert the cluster-wide rewrite.",
  evidence="Rotation lists every active deployment with no space or visibility filter and appends a deployment mutation per match in the same commit, authored as the caller and bypassing the deployment handlers and their host-access gate. RefDetails renders every referencing deployment's space, node and name into the client-visible message, and panics on a query error inside the write transaction.",
  validation="Read at ef1f3508. Count mismatches surface only as a fixed 409, and an expected_seq of 0 skips the staleness check.",
  sources=[["backend/app/primary/domain/values/references.go", 92, 105], ["backend/app/primary/domain/values/references.go", 60, 69], ["backend/app/primary/domain/deployments/refs.go", 131, 161]]),

"RR-16": dict(
  recheck="Still open. The installer repeats the same modes.",
  validation="Read at ef1f3508; the unconditional chmod is in the goutil helper the roots are created with.",
  sources=[["backend/ainit/ainit.go", 68, 85], ["backend/lib/engine/runner/container.go", 1124, 1135]]),

"RR-17": dict(
  recheck="Still open.",
  validation="Read at ef1f3508.",
  sources=[["backend/app/installer/installer.go", 107, 113]]),

"RR-18": dict(
  recheck="Still open for reveals. One statement from September no longer holds: settings changes and user creation now carry an author in the write log.",
  impact="Reveal authorises, decrypts and returns with no log line and no row. Writes are recorded in the write log with an author, but reads leave no trace, so a compromised session can drain a space's secrets undetected. The mux exposes an Audit callback that nothing wires up or calls.",
  evidence="PostV1SecretsReveal and RevealByRef contain no logging. MuxConfig.Audit is declared, never set in run.go and never invoked by the generated mux, and the mux writes no access log. Writes made from a root session are recorded as author 0, so they cannot be attributed to a particular root session.",
  validation="Read at ef1f3508.",
  sources=[["backend/app/primary/webuihandler/secrets.go", 274, 290], ["backend/apigen/mux.gen.go", 20, 24]]),

"RR-19": dict(
  recheck="Still open; the maintenance, free-space and writer code the finding rests on is untouched.",
  validation="Read at ef1f3508. Whether the agent stays up on a full volume was not traced through the collector and commit paths.",
  sources=[["backend/lib/log/logmanager/maintenance.go", 17, 24], ["backend/lib/log/logmanager/maintenance.go", 109, 112], ["backend/lib/log/v2/writer.go", 38, 54]]),

"RR-20": dict(
  recheck="Still open. The DNS catalog now comes only from the published map, which emits one entry per deployment under the lossy label.",
  validation="Traced in source at ef1f3508; not exercised.",
  sources=[["backend/lib/network/names.go", 10, 31], ["backend/app/primary/domain/deployments/validate_layers.go", 154, 164], ["backend/app/netproxy/netstate.go", 189, 215]]),

"RR-21": dict(
  title="Operator-authored wildcard rules silently gained the host verbs",
  recheck="Still open and still conditional. auth.md states that no custom-rule migration accompanies the host permissions. RR-27 is the same shape for a newer verb.",
  impact="An all-verbs-excluding selector matches any verb not on its list, and start-up rewrites only the two built-in templates. Any template, direct-rule grant or allow-mode global rule an operator wrote before the change with such a selector now grants use_host_mounts and use_host_network to its holders without anyone having decided that.",
  evidence="verbMatches returns true for an all_verbs_excluding selector unless the verb is listed; authz.Open upserts only the built-in templates (ids 1 and 2). No migration at any point added the host verbs to stored exclusion lists.",
  validation="Inference from the evaluator and the start-up code at ef1f3508; no live cluster was inspected.",
  fix="Add the two verbs to the exclusion list of every persisted all-verbs-excluding selector, and require new dangerous verbs to be opted into explicitly rather than matched by wildcard.",
  sources=[["backend/app/primary/domain/authz/evaluate.go", 50, 58], ["backend/app/primary/domain/authz/authz.go", 69, 81]]),

"RR-22": dict(
  recheck="Still open; the filter rules are unchanged. Non-cluster ULA space is likewise not denied on IPv6.",
  validation="Read at ef1f3508; not checked on a Linux node.",
  sources=[["backend/lib/network/filter.go", 37, 42], ["backend/lib/network/filter.go", 157, 194]]),

"RR-23": dict(
  title="Unauthenticated enrollment creates permanent node rows, throttled only per source address",
  recheck="Still open, with a correction: the September text said there was no rate limit. A per-address limiter (one hello per five seconds, burst five) has been on the enrollment listener since July. Rows are still permanent, and each hello now also appends commits to a write log that is never compacted.",
  impact="A first hello inserts a node row and ending the request only flips its status; nothing deletes it. The enrollment listener has no client authentication, no cap on pending requests and no global limit, so anyone reaching :9444 from many addresses can mint keys and grow the node table and the write log without bound. The CSR requirement adds one keygen per row.",
  evidence="UpsertEnrollmentRequest creates a node on first sight; EndEnrollmentRequest sets ENROLLMENT_CANCELLED or EXPIRED and keeps the row. The only limit is PerIP at 0.2 requests per second with a burst of 5, keyed on the exact source address.",
  validation="Traced in source at ef1f3508.",
  fix="Cap the number of pending enrollments, add a global hello budget, key the limiter on a /64 for IPv6, and garbage-collect cancelled and expired rows after a grace period.",
  sources=[["backend/app/primary/domain/nodes/enrollment.go", 49, 63], ["backend/app/primary/domain/nodes/enrollment.go", 171, 193], ["backend/app/primary/run.go", 120, 122]]),

"RR-24": dict(
  recheck="Still open; lifetimes and the absence of any revocation list are unchanged.",
  validation="Traced in source at ef1f3508.",
  sources=[["backend/util/certu/sign.go", 55, 58], ["backend/lib/issuedtls/issuedtls.go", 10, 10]]),

"RR-25": dict(
  recheck="Still open. Delete and move protection were moved onto the shared collectors, with a comment noting the env-only scan missed ingress certificate secrets; rotation was not moved over. The rotation dialog's reference count has the same blind spot.",
  validation="Traced in source at ef1f3508.",
  sources=[["backend/app/primary/domain/values/references.go", 130, 156], ["backend/lib/engine/prepare/runtimeinputs/secrets.go", 252, 260]]),

"RR-26": dict(
  recheck="Still open; the package is unchanged and has no tests.",
  evidence="tarPath applies the flake-supplied regex rewrite to the destination and splitPath cleans the result and splits on slashes. Cleaning collapses dot segments inside an absolute path, but a rewrite that yields a relative path with leading .. survives and is emitted as entry names. Validation requires only that the source is a store path and that the regexes compile.",
  validation="Inference from reading tar.go at ef1f3508; no malicious layer was built.",
  sources=[["backend/lib/engine/prepare/nix2container/tar.go", 75, 104]]),
}

RECHECK_NEW = [
f(id="RR-27", origin="recheck", severity="High", area="Authorization / enrolment", status="Reproduced against HEAD; conditional",
  title="user:update alone issues a sign-in link for any account, including a cluster admin",
  cwe="CWE-269 / CWE-863",
  impact="The route that issues enrol and recovery links checks only update on the user type in the system space, and never the target. A holder of that one verb can issue a link for a cluster administrator, redeem it on the unauthenticated redeem route and hold a full session as that administrator. auth.md documents the requirement as create on both user and access, and the rule explainer in the Users page shows the user type as view-only, so an administrator granting the verb is told it conveys nothing.",
  trigger="A human session whose grants cover update on user in space 0 without access management, for example a custom rule of all verbs on everything except access. Among the built-in roles only cluster_admin holds it, so an install that uses only the built-ins is not exposed. Applies to the unreleased bckup-1 branch; the route does not exist in v0.0.617.",
  evidence="PostV1UsersLink calls requireEnrolAccess with update on user only, while the sibling create route requires create on user and on access. The check runs with entity id 0, so a selector that excludes a specific user still matches. Redemption opens a full session for the link's target.",
  validation="Reproduced on 22 November (TestRecheckUserUpdateMintsSignInLinkForAnAdmin): a user holding only user:update is refused access:create, issues a link for the admin, redeems it, and the resulting session passes the access:create check. Whether any live cluster holds such a grant was not inspected.",
  fix="Gate link issuance as documented (create on user and on access), or check the verb against the target user and refuse targets that hold access-managing grants. Bring the explainer tables and auth.md into line with what the handlers check, and treat user:create and user:update as verbs that existing wildcard rules must opt into (see RR-21).",
  close="A caller without access:create must be refused a link for any user, and the handler, auth.md and the explainer must agree on the verbs the user type carries.",
  sources=[["backend/app/primary/webuihandler/enrol.go", 118, 143], ["backend/app/primary/webuihandler/enrol.go", 39, 57], ["backend/app/primary/domain/authz/evaluate.go", 80, 88]]),

f(id="RR-28", origin="recheck", severity="High", area="Dependencies / availability", status="Scanner and release binary verified",
  title="The release toolchain and HTTP modules carry denial-of-service advisories reachable on the public listeners",
  cwe="CWE-1395 / CWE-400",
  impact="The published v0.0.617 binary is built with Go 1.27.1 and golang.org/x/net v0.55.0. Advisories fixed in Go 1.27.2 to 1.27.4 and x/net v0.60.0 let an unauthenticated client exhaust memory through HTTP/2 Trailer headers, crash an HTTP/2 server through an HPACK encoder race, and burn CPU with repeated window updates. The affected servers are the Web UI, the cluster listener and the netproxy ingress that fronts every tenant's HTTPS traffic. The bundled containerd daemon and client library are 2.3.5, one advisory behind (image-pull amplification through a crafted OCI index, fixed in 2.3.6), which a deployer can trigger by pointing a deployment at a registry they control.",
  trigger="Network reachability of :443 on the primary or of any ingress listener. For the containerd advisory, create or update permission on a deployment.",
  evidence="go.mod declares go 1.27.0 with no toolchain line and the release workflow asks setup-go for 1.27, so the patch level is whatever was current on the build day; go version on the downloaded v0.0.617 linux/amd64 asset reports go1.27.1. govulncheck reports 22 advisories with symbol-level traces on both targets, up from 3 in September, with traces into the netproxy HTTPS server, the generated mux and the cluster server. OD-10 compounds this: none of the listeners sets a header or handshake deadline.",
  validation="govulncheck v1.8.0 run on 22 November against the 11 November database for host and Linux/amd64 with Go 1.27.0; the release asset's embedded toolchain and module versions read with go version -m. No exploit was run; the ECH, CONNECT and MaxBytesReader advisories have preconditions that were not established for OpenDeploy's handlers.",
  fix="Pin the release toolchain to the latest 1.27 patch with a toolchain line in go.mod, bump golang.org/x/net, containerd/v2 with the bundled daemon and runc, grpc, x/text, klauspost/compress, brotli and moby/sys/user, and add govulncheck to the release workflow so a reachable advisory fails the build.",
  close="govulncheck must report no symbol-level advisory for either target, and the release binary's go version must be the current patch release.",
  sources=[["backend/go.mod", 1, 4], [".github/workflows/build.yml", 50, 53], ["backend/lib/runtimebin/runtimebin.go", 44, 64]],
  refs=[["GO-2026-6603", "https://pkg.go.dev/vuln/GO-2026-6603"], ["GO-2026-6617", "https://pkg.go.dev/vuln/GO-2026-6617"], ["GO-2026-6611", "https://pkg.go.dev/vuln/GO-2026-6611"], ["GO-2026-6597", "https://pkg.go.dev/vuln/GO-2026-6597"]]),

f(id="RR-29", origin="recheck", severity="Medium", area="Cluster protocol / availability", status="Reproduced against HEAD",
  title="One status report with an epoch timestamp from a worker makes the opening snapshot unencodable",
  cwe="CWE-20 / CWE-754",
  impact="A worker's status report is validated only for the presence of updated_at. A report whose timestamp is the Unix epoch is stored as clock 0, and 0 reads back as absent, so every later opening snapshot contains a status without its required field and fails to encode. Every user who can see that instance, which includes every administrator, loses the event stream and with it the Web UI, until the row is repaired outside the UI. Workloads keep running.",
  trigger="Root on one enrolled worker, and a scheduled instance on that node that has no status yet, which every new deployment version placed there provides. Present in v0.0.617.",
  evidence="DecodeTimestamp of an empty submessage yields a present time at the epoch. handleStatusWrite checks only that the instance belongs to the calling node. The clock is stored as UnixNano and nanosToClock maps 0 to not present, so the row read back for the snapshot fails the generated required-field check and EncodeChecked ends the stream; the non-stream route answers 500 the same way. An instance that already has a positive clock cannot be poisoned, and evicting the node writes a newer row.",
  validation="Reproduced on 22 November (TestRecheckEpochStatusClockBreaksTheOpeningSnapshot): the snapshot encodes before the report, the report passes request validation and commits, and the next opening snapshot fails with \"scheduled_instance_status.updated_at: is required\". The effect on the frontend was not run in a browser.",
  fix="Reject node-reported clocks that are zero, negative or far in the future in handleStatusWrite, store presence separately from the clock value, and make the snapshot drop and log an entity that fails validation instead of failing as a whole.",
  close="A status write with an epoch updated_at must be refused, and a test must show the opening snapshot still encodes afterwards.",
  sources=[["backend/app/primary/clusterhandler/session.go", 284, 293], ["backend/storage/primarydb/pq/clock.go", 9, 21], ["backend/app/primary/domain/scheduledinstances/status.go", 83, 96]]),

f(id="RR-30", origin="recheck", severity="Medium", area="Cluster protocol / availability", status="Source verified",
  title="A worker can grow the primary's write log without bound, and worker-driven writes panic the primary on a commit error",
  cwe="CWE-770 / CWE-248",
  impact="Every status report with a clock no older than the last one, and every hello, is one commit appended to a write log that is never compacted, fanned out to the publisher and every open browser stream. The status and hello strings have no length rules, so a single message can be close to the 16 MB frame limit. A worker that loops them fills the primary's disk, and its backups. The helpers on this path panic on any commit error from a goroutine with no recover, so the first failed commit takes the whole primary down, and it cannot restart cleanly while the disk is full.",
  trigger="Root on one enrolled worker. Present in v0.0.617.",
  evidence="handleStatusWrite and handleClusterHello commit on every frame with no deduplication, rate limit or size limit beyond MaxRequestBodySize. WriteReplicatedStatus, ReportNode and UpdateNodeObservedMeta panic on a commit error, and the session reads frames on a bare goroutine. No statement deletes from write_event_mutations.",
  validation="Traced in source at ef1f3508. No input was found that makes a commit fail deterministically, so the crash is tied to disk-full or busy conditions; nothing was executed.",
  fix="Add length rules to the status and hello strings in the contract, drop a status that equals the stored row apart from its clock, rate-limit frames per session, and return or recover commit errors on worker-driven paths so only that session closes. Longer term, compact observed-status history.",
  close="Oversized or high-rate status and hello frames from one session must be rejected or coalesced, and a commit error while handling a worker frame must close only that session.",
  sources=[["backend/app/primary/clusterhandler/session.go", 222, 254], ["backend/app/primary/domain/scheduledinstances/status.go", 70, 80], ["backend/app/primary/clusterhandler/session.go", 124, 133]]),

f(id="RR-31", origin="recheck", severity="Medium", area="Authentication / availability", status="Source verified",
  title="Password change verifies Argon2 inside the global write lock, outside the credential rate limits",
  cwe="CWE-667 / CWE-307",
  impact="The self-service password route verifies the current password inside the commit callback, so each attempt holds the store's write mutex and an open transaction for one 64 MiB Argon2 computation. The route is not under the credential limiter, only the general 40 requests per second per address. Any signed-in user, with no grants at all, can queue attempts faster than they are served and stall every other write: sign-ins, deployment changes, node status reports and stream openings. The same route answers a wrong current password with a distinct error, so a stolen session token can test passwords for the account far faster than the login route allows.",
  trigger="Any human session on an account that has a password; a user can set one first. Applies to the unreleased bckup-1 branch; the route does not exist in v0.0.617.",
  evidence="PostV1AccountPassword calls authu.VerifyPassword inside the function passed to users.Update, which runs as the mutate step of state.Commit with the mutex held. Opening a stream snapshot takes the same mutex. The prefix limiters in run.go cover /v1/auth routes only.",
  validation="Lock scope, limiter coverage and the distinct error were read at ef1f3508. Hash duration and the resulting queue growth were not measured, so the availability effect is unquantified.",
  fix="Verify the current password before Commit, then re-read the stored hash inside the commit and abort if it changed. Put /v1/account/password under the same limiter as password login, and under the semaphore OD-09 recommends.",
  close="A burst of wrong-current requests must not delay an unrelated commit, and must be throttled like login attempts.",
  sources=[["backend/app/primary/webuihandler/account.go", 79, 96], ["backend/storage/primarydb/state/commit.go", 20, 34], ["backend/app/primary/run.go", 129, 139]]),

f(id="RR-32", origin="recheck", severity="Medium", area="Sessions", status="Source verified",
  title="An open event stream outlives its session's revocation and expiry, including the ten-minute root session",
  cwe="CWE-613",
  impact="A session is validated once, when the stream request arrives. Revoking the session, or letting it expire, stops new requests with that token, but a stream that is already open keeps delivering every commit the user's grants allow: deployment specs, config values, secret metadata, nodes and grants. For a root session the stream keeps delivering the user roster and every grant past the ten minutes that are meant to bound it. The stream ends only when the client disconnects or the primary restarts, and the listeners set no write or idle timeout (OD-10).",
  trigger="A token that was valid when the stream was opened: a stolen user or agent token, or a root session someone left open.",
  evidence="VerifyAuth resolves the session at request start and records its expiry on the context, but the stream loop selects only on client disconnect, updates, side channels and a five-second heartbeat. Grant changes do reset the stream; the session row is never re-checked and no timer is armed at the expiry.",
  validation="Read at ef1f3508; the same loop is in v0.0.617 for agent sessions. Not executed.",
  fix="Arm a timer at the session's expiry, and close the stream when a mutation revokes the connection's own user or agent session.",
  close="Revoking a session must close its open streams within one commit, and no stream may outlive its session's expiry.",
  sources=[["backend/app/primary/webuihandler/state_stream.go", 149, 172], ["backend/app/primary/webuihandler/auth.go", 161, 177]]),

f(id="RR-33", origin="recheck", severity="Medium", area="Assets / integrity", status="Source verified",
  title="Asset content is served and installed without checking it against the recorded sha256",
  cwe="CWE-345 / CWE-494",
  impact="Since asset content moved out of the database, the primary streams an asset from the large-asset root or from S3 by storage key and the worker writes whatever arrives into its cache. The sha256 recorded in the write log is not checked on either side, and the worker has nothing to check against because the reference carries no digest. Anyone who can overwrite the object in the bucket, or the file on the primary, substitutes the content of files and executables mounted into every deployment that uses the asset, and the substituted copy stays in each worker's cache for that version.",
  trigger="Write access to the asset bucket (by default the backup bucket) or to the large-asset root on the primary. No database change or restore is needed, which distinguishes this from RR-14.",
  evidence="OpenAsset opens the local file or the S3 object and returns the database size. GetV1ClusterAsset copies the body to the worker. EnsureAssetsReady trusts an existing cache file by name and otherwise copies the stream to a temporary file and renames it. The reconciler's S3-to-local copy does verify size and sha256, so the check exists but is not on the serving path.",
  validation="Traced in source at ef1f3508; no tampered object was served. S3 credentials stay on the primary.",
  fix="Hash while streaming in OpenAsset and fail the transfer on a mismatch, send the sha256 to the worker in the projection or a header and verify before the rename, and verify cached files at least once per agent start.",
  close="A worker must refuse to install an asset whose bytes do not match the sha256 recorded for that version.",
  sources=[["backend/app/primary/domain/assets/store.go", 267, 295], ["backend/app/primary/clusterhandler/handler.go", 186, 197], ["backend/lib/engine/prepare/runtimeinputs/assets.go", 36, 52]]),

f(id="RR-34", origin="recheck", severity="Low", area="Cross-space disclosure", status="Source verified",
  title="Write-time validation discloses other spaces' names and probes their certificate secrets",
  cwe="CWE-200 / CWE-209",
  impact="A caller who may create or update a deployment in one space can submit references to value ids in any other space and read the refusal: it names the secret, config or asset and its space. For an ingress certificate reference the validator decrypts the referenced secret before any space check and reports whether it holds a PEM pair, whether it covers a hostname the caller chose, and when it expires. Separately, creating a network policy answers \"redundant\" before either access check, which maps deployment ids to spaces for a caller with no grants at all. The same class as the name leaks in RR-03 and RR-15, through different sinks.",
  trigger="Create or update on a deployment in any one space, agent sessions included; for the network-policy case, any signed-in session.",
  evidence="validateCertSecret reveals the secret by reference in the pre-lock validation and returns distinct messages for an unreadable value, a non-PEM value, a hostname mismatch and an expired certificate; system-space secrets are excluded from that path. validateRefSpaces formats the referenced entity's key and space into DisplayErr. validateNetworkPolicyContent compares the two peers' spaces and returns RedundantErr before calling requireEntityAccess.",
  validation="Messages and call order read at ef1f3508; not executed. The network-policy ordering predates the rewrite.",
  fix="Check the reference's space before touching the value, return the same message for an entity the caller cannot view as for an unknown id, and run the access checks before the same-space comparison.",
  close="A reference to an entity the caller cannot view must read the same as an unknown id on every write route.",
  sources=[["backend/app/primary/domain/deployments/validate.go", 383, 402], ["backend/app/primary/domain/deployments/validate_refs.go", 18, 35], ["backend/app/primary/webuihandler/network_policies.go", 57, 77]]),

f(id="RR-35", origin="recheck", severity="Low", area="Agent sessions", status="Source verified",
  title="An agent's event stream carries its owner's other agent-session rows, including the pickup secret",
  cwe="CWE-200",
  impact="Agent-session rows are filtered by owner only, so a delegated connection receives every agent-session row of the user it acts for. The row carries the session id, which is the secret the unauthenticated pickup route is keyed on. An agent watching the stream can collect the token of the next request its owner approves before the intended agent does, and so hold a fresh six-hour token that survives the revocation of its own session. The authority gained is the same delegated authority it already had, for longer.",
  trigger="An approved agent token, and the owner approving another agent request while it is connected.",
  evidence="The visibility rule for agent sessions compares the row's user id with the connection's and has no exclusion for delegated connections. The stream entity includes SessionID, which the handler describes as the pickup secret. Listing agent sessions is human-only and a delegated revoke is limited to its own session, so the stream is the only place an agent sees its siblings.",
  validation="Read at ef1f3508; the filter predates the rewrite. Not executed.",
  fix="Hide agent-session and user-session rows from delegated connections, or strip the session id from rows that have not been collected.",
  close="A delegated stream must carry no session row other than, at most, its own.",
  sources=[["backend/app/primary/webuihandler/visibility.go", 300, 306], ["backend/storage/primarydb/pq/agent_sessions.go", 44, 50], ["backend/app/primary/webuihandler/agent_sessions.go", 138, 140]]),

f(id="RR-36", origin="recheck", severity="Low", area="Cluster protocol", status="Source verified",
  title="A node keeps a retired instance's entitlements for as long as it withholds the final status",
  cwe="CWE-672",
  impact="A node may fetch secrets, configs, assets, GitHub credentials and issued TLS for every instance of its own that is not finalized, and an instance is finalized only when the node itself reports it stopped. A compromised node that never reports the final status after a deployment is stopped or moved away keeps minting certificates for that deployment's identity and keeps fetching the current GitHub token and renewed ACME bundles for its hostnames, until an administrator evicts the node.",
  trigger="Root on one enrolled worker that has hosted the deployment.",
  evidence="finalizeStopped moves an instance from TERMINATE to FINALIZED only when the runner status the node reported is STOPPED or NO_DEPLOYMENT; there is no deadline. The entitlement set is every instance whose state is not FINALIZED. Eviction finalizes the node's instances.",
  validation="Traced in the scheduler and the cluster handler at ef1f3508. Whether the UI makes a stuck terminating instance obvious was not checked.",
  fix="Finalize a terminating instance after a deadline whatever the node reports, or key entitlement on the instance being wanted plus a short grace period.",
  close="An instance in TERMINATE must stop granting cluster-route access within a bounded time.",
  sources=[["backend/app/primary/scheduler/scheduler.go", 322, 335], ["backend/storage/primarydb/pq/scheduled_instances.go", 199, 201]]),

f(id="RR-37", origin="recheck", severity="Low", area="Authentication / upgrade", status="Source verified",
  title="An install that had password login off accepts passwords after the upgrade",
  cwe="CWE-1188",
  impact="The old setting auth.password_login_enabled is accepted and ignored, and the new auth.require_passkey defaults to off when it is absent from stored settings. An install that ran passkey-only therefore starts accepting per-user password sign-in on the public listener as soon as any account has a password, and any session on a passwordless account can set one without re-authentication (RR-10).",
  trigger="An upgrade of an install that ran with password login off, followed by a user, or a holder of that user's token, setting a password. Applies to the unreleased bckup-1 branch.",
  evidence="FillClusterSettingDefaults fills require_passkey with false; nothing maps the old field to the new one. Password login is refused only when require_passkey is on. upgrade_log.md has no entry for the change.",
  validation="Read at ef1f3508; no upgrade was run.",
  fix="At the first start after the upgrade, set require_passkey when the stored password_login_enabled was false and at least one passkey exists, or call the change out in the upgrade log.",
  close="An install that had password login off must still refuse password sign-in after the upgrade until an administrator opts in.",
  sources=[["backend/apigen/model_ext.go", 426, 435], ["backend/app/primary/webuihandler/password.go", 72, 75]]),
]

RECHECK_CLOSED = [
  dict(id="RR-13", prior="Medium", title="Space ids are reused after delete and nothing is cleaned up",
       how="Closed in v0.0.617 by f22f158 and 87ae82c, confirmed on 22 November. Ids for every entity type now come from a per-type counter that only moves forward and is rebuilt from the never-compacted write log, so deleting the highest space no longer frees its id. DeleteSpace refuses, under the write lock, while the space still has a deployment, secret, config, asset, folder or network policy, or is named by an access grant, a grant template or a global rule, and the refusal says what stands in the way. A closure check deletes the highest space and gets a new id for the next one, and is refused the delete of a space holding a secret or named by a grant (TestRecheckSpaceIDNotReusedAndDeleteRefusedWhileReferenced); the project's own TestSpaceDeleteIsRefusedWhileInUse covers each kind. Not verified: an install that deleted a space before the v0.0.616 conversion, whose conversion code is no longer in the tree.",
       commit=RECHECK_COMMIT,
       sources=[["backend/storage/primarydb/pq/materialise.go", 62], ["backend/app/primary/domain/nodes/spaces.go", 162], ["backend/app/primary/domain/nodes/spaces.go", 102], ["backend/app/primary/domain/authz/space_refs.go", 21]]),
  dict(id="OD-12 (part)", prior="Medium", title="Passkey login updates append duplicate credentials and retain stale state",
       how="Closed in v0.0.614 by 80b8fbd, confirmed on 22 November. The shared save callback replaces the credential with the same id in place and stamps it as used, so the record no longer grows and the verifier compares against the latest counter. A closure check saves one credential twice through the service callback and finds one row with the second counter (TestRecheckPasskeySaveReplacesByCredentialID). The clone warning is still never acted on; that half stays open as OD-12 at Low.",
       commit=RECHECK_COMMIT,
       sources=[["backend/app/primary/domain/users/users.go", 174], ["backend/app/primary/webuihandler/passkey.go", 60]]),
]


def _apply_recheck():
    by_id = {x["id"]: x for x in FINDINGS}
    unknown = set(RECHECK_UPDATES) - set(by_id)
    assert not unknown, unknown
    for fid, changes in RECHECK_UPDATES.items():
        by_id[fid].update(changes)
    FINDINGS[:] = [x for x in FINDINGS if x["id"] not in RECHECK_CLOSED_IDS] + RECHECK_NEW
    missing = [x["id"] for x in FINDINGS if x["origin"] != "recheck" and not x.get("recheck")]
    assert not missing, missing
    CLOSED[:0] = RECHECK_CLOSED


# ================================================================ 23 November 2026 fixes
# RR-27, RR-28 and most of RR-05 were fixed on bckup-1 on 23 November, in
# commits ca3ff94, 12f2d8c and fa23e93; 73f163c adds the upgrade notes.
# The commits are not pushed: a source whose commit or fourth element is
# FIX_COMMIT is read from that commit and linked to the local file. Applied by _apply_fixes() after _apply_recheck().
FIX_COMMIT = "73f163c6671d498cb7038375bcd8a49db93ffe8b"
FIX_DATE = "23 November 2026"
FIX_CLOSED_IDS = {"RR-27"}

FIX_UPDATES = {
"RR-21": dict(
  update="RR-27, cited in the note above, was closed on 23 November. The fix for RR-05 adds a third host permission, use_privileged_ports, and a custom rule written as all verbs except a list picks it up the same way. This finding is otherwise unchanged."),

"RR-05": dict(
  severity="Medium", prior="High", status="Narrowed; fix committed, not released",
  title="A port forward can still take a port above 1023 that a host service uses",
  update="Most of this finding was fixed in commit fa23e93, not released; that part is in the closed table as RR-05 (part). What remains is narrower and is described here.",
  impact="A forward claims its port on every address of the node. The platform now refuses a port another deployment forwards and its own listeners, and requires a cluster-admin permission below 1024, but it cannot know about other services the host runs. A deployer can still forward a port of 1024 or above that such a service uses, for example an sshd moved to 2222, a database or a monitoring agent, and traffic arriving for that port on the node's addresses then goes to the deployer's container. Connections to the service over loopback are no longer affected.",
  trigger="Create or update permission on a deployment in any space, and a host service listening on a port of 1024 or above on a node the deployment can be placed on.",
  evidence="The reservation list is built from the cluster settings and the WireGuard port only, and the permission check looks at the port number. A forward's prerouting rule matches fib daddr type local, so it takes precedence over a socket the host has bound on the same port. Nothing lets an administrator name further ports to protect.",
  validation="Read at commit 73f163c on 23 November. The closed part is covered by unit tests, a closure check, a kernel test run in a Linux container and a passing run of the VM harness; the residual was not executed.",
  fix="Let an administrator reserve further host ports per cluster or per node, or allow non-admin forwards only inside a configured port range.",
  close="A deployer without a host permission must not be able to forward a port the administrator has reserved for a host service.",
  sources=[["backend/app/primary/domain/deployments/ingress_plan.go", 15, 21, FIX_COMMIT], ["backend/app/primary/webuihandler/deployments.go", 132, 153, FIX_COMMIT]]),

"RR-28": dict(
  status="Fixed in 12f2d8c; not released",
  update="Fixed in commit 12f2d8c, not released. go.mod now requires Go 1.27.4, so a build uses that patch release or a newer one. golang.org/x/net, x/text, grpc, containerd/v2, klauspost/compress, brotli, moby/sys/user and the S3 client are bumped. The bundled runtime pins move to containerd 2.3.7 and runc 1.5.3, with checksums that match the ones published with each release. The release workflow asks setup-go for the latest patch and runs govulncheck for the target architecture before the build. govulncheck now reports no advisory at symbol or module level for host, Linux/amd64 and Linux/arm64. The finding stays open because v0.0.617 is still the published release, and the second closure criterion needs a release binary. The VM harness passed on 23 November with the nodes running containerd 2.3.7 and runc 1.5.3.",
  impact="The published v0.0.617 binary is built with Go 1.27.1 and golang.org/x/net v0.55.0. Advisories fixed in Go 1.27.2 to 1.27.4 and x/net v0.60.0 let an unauthenticated client exhaust memory through HTTP/2 Trailer headers, crash an HTTP/2 server through an HPACK encoder race, and burn CPU with repeated window updates. The affected servers are the Web UI, the cluster listener and the netproxy ingress that fronts every tenant's HTTPS traffic. The containerd daemon that release bundles, and its client library, are 2.3.5, one advisory behind (image-pull amplification through a crafted OCI index, fixed in 2.3.6), which a deployer can trigger by pointing a deployment at a registry they control.",
  evidence="At ef1f3508 go.mod declared go 1.27.0 with no toolchain line and the release workflow asked setup-go for 1.27, so the patch level was whatever was current on the build day; go version on the downloaded v0.0.617 linux/amd64 asset reports go1.27.1. govulncheck reported 22 advisories with symbol-level traces on both targets at that commit, up from 3 in September, with traces into the netproxy HTTPS server, the generated mux and the cluster server. OD-10 compounds this: none of the listeners sets a header or handshake deadline. The excerpts below are commit 73f163c, which carries the fix.",
  validation="govulncheck v1.8.0 run on 22 November against the 11 November database for host and Linux/amd64 with Go 1.27.0; the release asset's embedded toolchain and module versions read with go version -m. No exploit was run; the ECH, CONNECT and MaxBytesReader advisories have preconditions that were not established for OpenDeploy's handlers. On 23 November the same scanner and database were run on the fix with Go 1.27.4 for host, Linux/amd64 and Linux/arm64 and found nothing, the 58 backend packages passed, and the four runtime checksums were compared with the upstream checksum files. The VM harness passed against the new runtime. The workflow change has not run in CI.",
  fix="Cut a release from 12f2d8c or later. Tell operators that every node restarts containerd once when it takes that release.",
  sources=[["backend/go.mod", 1, 3, FIX_COMMIT], [".github/workflows/build.yml", 50, 54, FIX_COMMIT], [".github/workflows/build.yml", 62, 68, FIX_COMMIT], ["backend/lib/runtimebin/runtimebin.go", 44, 69, FIX_COMMIT]]),
}

FIX_CLOSED = [
  dict(id="RR-05 (part)", prior="High", title="Port forwards can claim reserved and co-tenant host ports",
       how="Fixed in commit fa23e93 on 23 November; not released. A save is now refused when another deployment on the node forwards the same protocol and port, with UDP checked for the first time, and when the port belongs to the platform: the Web UI, cluster and enrollment listeners on the primary and the WireGuard port on every node. A forward on a port below 1024, or a TLS passthrough route on one other than 443, needs the new use_privileged_ports permission, which cluster admins hold and space admins and agent sessions do not; it is checked on every update like the other host permissions. A forward's DNAT rules now exclude loopback. A closure check shows a second TCP or UDP forward and a forward on the WireGuard port rejected at write time and a space admin denied port 22 (TestRecheckPortForwardClaimsRejectedAtWriteTime); unit tests cover the evaluator, the handler on every update kind and the settings change. One statement of the finding was wrong: a kernel test on Linux 6.12 shows a forward did not capture loopback traffic, it made the host's own service on that port time out on 127.0.0.1 and ::1; with the exclusion the service answers (TestForwardLeavesHostLoopbackAlone, run in a container). The VM harness passed with the new rules on the forwards it creates. Two forwards that already share a port keep running and neither can be saved until one moves. Still open as RR-05 at Medium: a port of 1024 or above that a host service uses.",
       commit=FIX_COMMIT,
       sources=[["backend/lib/ingressplan/ingressplan.go", 239], ["backend/app/primary/domain/deployments/ingress_plan.go", 15], ["backend/app/primary/webuihandler/deployments.go", 122], ["backend/lib/network/nft_linux.go", 578], ["backend/app/primary/webuihandler/port_forward_claims_test.go", 22], ["backend/lib/netaudit/kernel_linux_test.go", 137]]),
  dict(id="RR-27", prior="High", title="user:update alone issues a sign-in link for any account, including a cluster admin",
       how="Fixed in commit ca3ff94 on 23 November. The route exists only on the unreleased branch, so no release carried the defect. PostV1UsersLink now requires create on user and on access, the gate user creation already had and the one auth.md documents. The 22 November reproduction no longer passes: the user holding only user:update is refused. A closure check gives a user every verb on the user type and no access management and finds it refused a link for a cluster admin, while an admin can still issue a link that redeems (TestRecheckSignInLinkRefusedWithoutAccessCreate). The project's own TestLinkIssueNeedsAccessCreate covers an admin target and a plain one. The rule explainer now lists create for the user type and says it also needs create on access, and the plan document's endpoint table matches. Two things were left as they are: the check is not target-aware, so issuing links cannot be delegated without access management, and user:update is now checked by no handler.",
       commit=FIX_COMMIT,
       sources=[["backend/app/primary/webuihandler/enrol.go", 121], ["backend/app/primary/webuihandler/enrol_test.go", 132], ["frontend/src/lib/authzExplain.js", 186], ["docs/future-work/user-enrolment-and-passwords.md", 157]]),
]


def _apply_fixes():
    by_id = {x["id"]: x for x in FINDINGS}
    unknown = (set(FIX_UPDATES) | FIX_CLOSED_IDS) - set(by_id)
    assert not unknown, unknown
    for fid, changes in FIX_UPDATES.items():
        by_id[fid].update(changes)
    FINDINGS[:] = [x for x in FINDINGS if x["id"] not in FIX_CLOSED_IDS]
    CLOSED[:0] = FIX_CLOSED


_apply_recheck()
_apply_fixes()
