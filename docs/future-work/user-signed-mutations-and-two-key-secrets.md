# User-signed mutations and two-key secrets

This note records an exploratory design that reduces the trust placed in the
primary. Users sign their own mutations, nodes verify what they are told to
run, and a secret needs two keys to open: one held by the primary and one held
by an identity (a user or a node).

This note extends [../engineering/secrets.md](../engineering/secrets.md),
[../engineering/auth.md](../engineering/auth.md), and the write contract in
[../engineering/api.md](../engineering/api.md).

## Status

Nothing in this note is implemented. No decision has been made to build it.
The note records a design discussion held on 2026-11-23, including the
conclusions reached, the positions that were corrected, and the questions left
open.

The recommended first steps are listed in [Recommended order](#recommended-order).

## Problem

The primary is the single trust point of a cluster.

- It applies every user action to the write log under its own authority. A
  session is an opaque bearer token, and nothing in the log proves that a user
  made a given change.
- Its Secrets Master Key (SMK) opens every secret. `secrets.md` records "root
  on the primary" as game over.
- Nodes run whatever the primary places on them and trust the `NodeProjection`
  they receive.

The goal is a primary that enforces the rules but cannot forge a user's
decision and cannot read a secret alone.

## Threat model

### Compromise classes

| Class | Examples |
|---|---|
| Passive or time-bounded | A database backup, a stolen disk, a VM snapshot, a read bug, an authorisation bypass in one handler |
| Active and persistent | Root on the primary held over time |

The design gives protection against the first class. Against the second class
it turns a silent total compromise into an outage plus an attack that must wait
for a privileged user.

### Limits

- **The primary serves the frontend.** The JavaScript bundle is embedded in the
  primary's binary. An attacker with root on the primary can replace it and
  wait for an administrator to sign in. A browser client therefore cannot
  resist an active, persistent attacker. Resisting one needs a signer outside
  the browser, such as a CLI.
- **Single-node installs gain nothing for secrets.** On a single-node install
  the primary is the node and holds both keys. The secrets model pays off only
  when the primary is a separate machine from the workloads.
- **A primary that runs workloads holds both keys** for the spaces those
  workloads belong to.

### Blast radius

| Compromised | Today | With two-key secrets |
|---|---|---|
| A node | The values its deployments reference | The same |
| The primary | Every secret | Nothing, for spaces whose workloads it does not run |
| A node and the primary | Every secret | Every space that node is eligible for |
| A node key and a database copy | Nothing | Nothing |

No case is worse than today. Space eligibility widens only the collusion case,
which is total today.

## Layers

The design has three layers. Each depends on the one before it.

| Layer | Content | What it changes |
|---|---|---|
| 1. Session signatures | Each session holds a key pair; each request is signed; the signature is stored in the write log | A stolen bearer token cannot write. The log gains a signed audit trail. Trust in the primary is unchanged, because the primary binds the session key to the user at login |
| 2. User keys and node verification | Each user holds a long-term key pair; an authority chain records keys, grants, and node membership; nodes verify signatures before they run or decrypt | The primary cannot forge a deployment, a grant, or a node |
| 3. Two-key secrets | Each space has a key pair wrapped to its members; the SMK is an outer layer | The primary cannot read a secret alone |

Layer 3 depends on layer 2. The primary is the directory of public keys, so
without signed membership a compromised primary substitutes its own key for a
node's and receives the wraps. Layer 3 without layer 2 gives little beyond the
TPM-sealed machine key planned as Phase 3 in `secrets.md`.

## Keys

### Hierarchy

```text
value             --AEAD--> DEK (one per secret version)
DEK               --HPKE--> space public key, epoch e    identity half (inner)
                  --AEAD--> SMK                          primary half (outer)
space private key --HPKE--> each member: user key, node key, recovery key
user private key  --AEAD--> each credential of that user
```

### Intermediate keys

An intermediate key is a key that is wrapped by a key above it and wraps keys
below it.

- A secret value is encrypted once, under its data key (DEK). It is not
  encrypted per identity.
- The DEK is wrapped under an intermediate key. The intermediate key is wrapped
  once per identity.
- A membership change therefore costs one key wrap per intermediate key. No
  secret ciphertext is touched.
- The SMK is an intermediate key today. `secret_keyslots` is its recipient
  list, with `machine_key{node_id}` and `recovery_code{kdf_salt}` wrappings.
- The intermediate key and the two-key requirement are separate mechanisms. The
  hierarchy decides who holds the identity half. The second key is an extra
  layer.

This design is an SMK per space, with `user_key` and `node_key` keyslots
wrapped asymmetrically.

### Space key

- Each space has one key pair. The granularity is the space because one wrap
  per space is the cost of a membership change.
- The space key is a key pair, not a symmetric key. A writer encrypts to the
  public half and cannot read. This preserves `secret : create` without reveal.
- The space key has epochs. The `smk_version` column on a secret version is the
  existing slot for an epoch.

### User key

- A WebAuthn assertion cannot sign each mutation, because every assertion needs
  a user gesture. A per-assertion signature remains usable as a step-up for
  single high-value actions.
- The WebAuthn PRF extension yields a secret per credential. That secret
  unwraps the user's long-term private key at login. Not every authenticator
  and browser supports the extension.
- Each credential of a user is a keyslot on the user's private key, the same
  pattern as `secret_keyslots`.
- The user key certifies a non-extractable session key. The session key signs
  each request.
- Password login is the default sign-in. A password user's key must be derived
  in the client, so the password can no longer be sent to the server. This
  requires the deferred PAKE work or a split derivation, where one output of
  the client-side derivation authenticates and another unwraps the key.
- A user who loses every credential loses the private key. Another member
  re-wraps the space keys to the user's new key.

### Node key

A node already generates a TLS key before enrolment. Its enrolment identifier
is the SHA-256 of that key's SubjectPublicKeyInfo, and the worker logs it for
the operator to compare (`api.md`, Nodes).

### Recovery key

Each space key is also wrapped to a recovery key derived from the recovery
code. Without it, the loss of every user key loses the secrets.

## Two-key secrets

### Layer order

The identity half is the inner layer. The primary half is the outer layer.

- Layers are removed outer first, and the party that removes the last layer
  sees the plaintext. With the primary's layer inside, the primary would
  perform the final decryption and see the value.
- With the primary's layer outside, the primary removes its layer first and
  hands on a ciphertext it cannot read.
- The primary's layer sits on secret versions only. The wraps of a space
  private key to its members carry no primary layer, so a membership change
  needs nothing from the primary's key.

| Action | What changes | Needs the primary's key |
|---|---|---|
| Add a user or node to a space | The space private key is wrapped to the new member | No |
| Remove a member | A new space key epoch is wrapped to the remaining members | No |
| Write a secret version | The writer encrypts to the space public key; the primary adds its layer | To seal only |
| Read a secret version | The primary removes its layer; the member decrypts | Yes |
| Move a secret to another space | A read, then a write under the destination space key | Yes |

The primary's key is used at two moments: sealing a new version and unsealing
one for a reader. A move is an unseal followed by a seal. Every read therefore
passes through the primary's authorisation check.

- **A membership change hands out the identity half only.** A member, or
  anyone a member wraps the space key to, reads nothing until the primary
  unseals for them.
- **The space key is the cryptographic boundary.** Distinctions inside a space,
  such as reveal on one secret but not another, or a node limited to the
  versions its instances pin, are enforced by the primary's check alone. They
  do not hold against a compromised primary.
- **A cross-space move changes behaviour.** Today a move rewrites the identity
  row and never re-encrypts. Under this design a member of the source space
  re-wraps the DEK of every version to the destination space key, so moving a
  secret requires holding the source space key.

Shamir's secret sharing is not used. It splits a secret among holders who
combine shares to reconstruct it, and the party that combines them sees the
secret. The access rule here is "the primary and any one member", which
layering expresses directly. Shamir's scheme fits one optional use: splitting
the recovery code among custodians.

### Read path

1. An identity requests a secret version.
2. The primary checks authorisation and removes the outer SMK layer.
3. The primary returns the value ciphertext and the DEK still wrapped to the
   space key.
4. The identity unwraps the space private key with its own key, unwraps the
   DEK, and decrypts the value.

The primary never holds the DEK or the plaintext.

### Write path

1. The writer generates a DEK and encrypts the value.
2. The writer wraps the DEK to the space public key.
3. The writer signs the version (see
   [Secret versions](#secret-versions)).
4. The primary adds the outer SMK layer and commits.

### What the primary half provides

The primary half is an online gate.

- A database copy plus a stolen identity key does not open a secret.
- Every decryption is an event the primary can log.
- Revocation takes effect for every version the identity has not yet fetched.

### Node fetch scope

The primary already limits a node to the secret references that its scheduled
instances pin (`GetV1ClusterSecrets` in
`backend/app/primary/clusterhandler/handler.go`, through
`allowedRefs(...).allSecretsAllowed`). The same check applies to removing the
outer layer. A compromised node alone therefore reaches the values its
deployments reference, as today.

### Access removal

| Case | Outcome |
|---|---|
| Versions the identity already fetched | Knowledge cannot be revoked. The remedy is rotating the credential itself. The primary's fetch log is the exact rotation list |
| Old versions the identity did not fetch | The primary refuses its half. No re-encryption is needed |
| Future versions | The space key moves to a new epoch, wrapped to the remaining members only |

A new epoch covers the case where a removed identity later colludes with a
compromised primary. It does not re-encrypt any secret: it is one new key pair
and one wrap per remaining member.

### Removed identities and forward secrecy

A node that once held a space key and kept it can read that space with the help
of a compromised primary, after its eligibility is removed. Two cases differ.

| Case | Remedy |
|---|---|
| The node was compromised while it was eligible | Rotating the credentials it could read. No key scheme helps, because it could copy the space key |
| The node was honest while eligible and is compromised later | The node deletes what it no longer needs, so a later compromise finds nothing |

The second case is closed without rotating any secret.

- **The node deletes the space key when it loses eligibility.** This follows
  the existing patterns: an evicted node wipes, and the retention sweep drops
  values no instance references.
- **Wraps are addressed to a rotating receive key.** The node's long-term key
  only signs. The node holds one receive key pair at a time, with a generation
  number, and publishes the public half signed by its long-term key. A member
  wraps a space key to the current generation, and the wrap names that
  generation.
- **The node rotates after each delivery.** It unwraps every wrap addressed to
  the current generation, stores the space keys in a local keystore sealed
  under its machine key, and publishes the next generation. It deletes the old
  private key once it has processed the write log past its own publication. The
  wraps in the log then open for nobody.
- **One receive key serves every space.** Several changes in quick succession
  address the same generation. The primary refuses a wrap that names a stale
  generation, and the client retries with the current one. The log's order
  therefore guarantees that no accepted wrap names a deleted key.
- **The node takes no part in the mutation.** The current receive key is always
  already published.
- **An editor never waits for a rotation.** A wrap does not use up the receive
  key. A generation becomes stale only when the next one is published, so a
  usable generation exists at every moment.
- **An offline node keeps its current private key** until it collects the wraps
  waiting for it.
- **Write order matters on the node.** It persists the unwrapped space keys
  before it deletes a receive private key, and persists a new private key
  before it publishes the public half.
- **A derived key does not give the same result.** If a member derives the
  node's next public key from a tweak that is visible in the log, a later
  compromise of the new private key yields the old one.

Re-encrypting existing versions under a new epoch does not close either case.
The write log is never compacted, so the old ciphertexts and their wraps
remain available to a compromised primary.

| Event | Action |
|---|---|
| Routine removal of a node from a space | The node deletes the space key. The space key moves to a new epoch |
| Suspected compromise of a node | The same, and the credentials that node could read are rotated |

The same reasoning applies to a user removed from a space: the user's client
rotates the user key pair at the next sign-in and re-wraps the space keys the
user still holds.

The design defends against a single compromised party and against the later
compromise of a party that was honest while authorised. It does not defend
against a party that was compromised while authorised, or against a compromised
primary that colludes with a current member of a space.

### Boundaries finer than a space

Inside a space the boundary is policy. Making it cryptographic means sealing
per deployment or per placement instead of per space.

| Granularity | Who seals for a node | Number of wraps | Work on change |
|---|---|---|---|
| Space (this design) | A member, at node approval | Spaces × (members + eligible nodes) | Membership changes only |
| Deployment version | The deployer's client, at signing, to every node eligible for the deployment | Live versions × (pinned secrets + eligible nodes) | Every deploy; every node join re-wraps for all live versions |
| Placement | An independent scheduler identity that decides placement and holds every space key | Placements × pinned secrets | Every placement, rollover, reschedule, and scale event |

- The placement form needs the party that decides placement to be the party
  that seals, and to be separate from the primary. Otherwise a compromised
  primary places any signed deployment on a colluding node.
- That identity holds every space key and is on the failover path. In a managed
  service it runs on the customer's side.
- Finer sealing narrows one case only: a compromised primary that colludes with
  a compromised node. It moves the concentration of keys from the nodes to the
  scheduler identity.

The space stays the cryptographic unit because it is already the unit of
authority. Accepted position AP-1 in `auth.md` gives anyone who can deploy in a
space every secret bindable from it. A customer who needs a hard boundary
between two sets of secrets uses two spaces with separate node eligibility.

A deployment may pin secrets from its own space or from the global space. Every
node that may run such a deployment therefore holds the global space key.

### Node enrolment and space eligibility

- The approving user's browser unwraps each space key it holds and wraps it to
  the node's public key.
- An approver can pass on only the keys they hold. Approving a node and letting
  a node run a space become separate acts.
- No user is online at failover. A node can run only what was wrapped to it
  beforehand, so node-to-space eligibility is a signed grant and the scheduler
  places a deployment only on eligible nodes.
- Node access policies, added on 2026-11-25, scope nodes to spaces today
  (`docs/product/deployments.md`). They are primary-trusted records, not
  signed grants.

### Conflicts with the current design

- **`PostV1SecretsGenerate`** produces the plaintext inside the primary. Under
  this model the primary sees a generated value before it is sealed.
- **Space 0 secrets** (cluster CA, web TLS, ACME account key) stay under the
  primary's key alone, because the primary needs them for unattended boot.
- **The cluster CA stays with the primary.** A node certificate alone must
  never earn a key wrap.
- **Total key loss.** If every user key and the recovery code are lost, nobody
  can sign a grant or a deployment. The secrets remain readable by eligible
  nodes, which hold the space keys, and the cluster keeps running without a way
  to change it. This is the cost of removing the primary as a trust root.

## Signed mutations

### What is signed

- The client signs the request, not the result. The primary assigns ids, the
  sequence, and the clock, so the client cannot sign the `CoreWriteUpdate`.
- The signature covers the exact request bytes, including `expected_seq`.
  Protobuf encoding is not canonical, so the stored bytes are the signed bytes
  and are never re-encoded.
- For the entity types that nodes act on, the client signs the entity payload
  itself. The payload in `write_event_mutations` is the entity document, and
  `seq`, `time`, and `actor` sit apart on `write_events`, so the server-filled
  fields mostly stay outside the signature.

### Who verifies

| Verifier | Result |
|---|---|
| The primary only | A tamper-evident log and sessions that a stolen bearer token cannot write with |
| Nodes as well | A compromised primary cannot make a node run an unsigned deployment or decrypt for an unsigned one |

This choice decides whether the design is an audit feature or a change of
trust model. It also decides what the log must carry.

### Write log

The log keeps its mutations and gains signatures. It does not become a log of
transactions. A transaction log would make every verifier re-execute handler
code, and old entries would have to replay identically under new code.

- **Size.** A signature and a session reference add about 100 bytes per user
  write. User writes occur at human rate.
- **One commit mixes authors.** The scheduler's `UpdateTrigger` appends its own
  mutations to the user's update, and the update carries one `actor`.
  Signatures are per mutation, and the trigger's mutations are attributed to
  the primary.
- **The log can never be rewritten.** The v0.0.616 conversion rewrote every
  payload once. A rewrite invalidates every signature. The payload evolution
  rule in `api.md` becomes a hard guarantee.
- **Replay.** A signed create carries no entity id, so the primary could apply
  it twice. Each signature commits to a recent log head (a sequence and a
  hash), which requires a hash-chained log. The head is a recent ancestor, not
  the immediate parent, because system writes advance the head continuously.
- **System writes.** Writes authored by the primary (scheduler, reconciler,
  observed state) are signed by the primary or left unsigned. The rules
  restrict them: the primary never authors a grant, a key wrap, or a deployment
  version.

## Node verification

A compromised primary could tell a node to run a malicious deployment version.
A user cannot sign every projection, and a node cannot verify a projection
without the log it was derived from. The node therefore verifies the contents
of the projection, not the projection.

### Content classes

| Content | Location | Decided by | Node check |
|---|---|---|---|
| What to run | The deployment version pinned on each `instances` row of `NodeProjection` | A user | The user's signature on that version |
| Who may decide | Not in the projection | Users, chained from a genesis key | The authority chain, held in full |
| Where and how it is wired | Placement, `net_map`, `acme` | The primary | Not verifiable; bounded by local rules |

A user signs a deployment version once, when they make it. The primary can
place that version in any projection but cannot make a new one.

### Hash pinning

For one signature to cover a deployment, the signed version pins everything it
references by content hash.

- **Assets** carry `sha256` already.
- **Secrets and configs** need a hash beside `{id, version}`.
- **Images and builds** need a digest. A mutable reference, such as an image
  tag or a branch, is a hole if the primary resolves it. Who resolves these
  today was not checked.

### Secret versions

Anyone can encrypt to a space public key. A compromised primary could forge a
secret version, for example a `DATABASE_URL` that points at its own server.
Each secret version therefore carries its writer's signature over the secret
id, the version, and the ciphertext hash. The client signs "latest + 1" and
`expected_seq` rejects a stale signature.

The writer's signature and the hash in the deployment version are both needed.

| Mechanism alone | Gap |
|---|---|
| Writer's signature | The secret id is server-assigned, so the first version cannot include it unless the client picks or reserves the id. The primary can present one secret's first version as another's |
| Writer's signature | Two writers racing, or a writer who edits after a failed save, both sign the same version number. The primary can serve either |
| Writer's signature | The node must evaluate the writer's grants as of signing time, including the secret's space-move history |
| Hash in the deployment version | The primary can forge a version and list it. The deployer's client pins the forged hash |

With both, the deployer's signature pins the exact bytes, and the deployer's
client checks the writer's authority when it pins. The node checks the hash and
that the signing key belongs to a known user.

### Authority chain

The authority chain is a hash-chained sequence of signed records: user keys,
grants, node membership, space eligibility, and space key wraps.

- It starts at a genesis key that every node pins at enrolment.
- It changes at human rate, so every node holds and verifies it in full.
- It is the only log a node must consume. Everything else reaches the node
  through the projection and is checked by signature or hash.

### Freshness

A signature proves that a user made a decision. It does not prove that the
decision is the user's latest. A compromised primary can still:

- **Freeze.** Withhold updates, including a revocation.
- **Roll back.** Replay an older signed version, such as one with a known
  vulnerability, or restart a deployment the user stopped.

Neither gives the attacker code execution or plaintext.

| Defence | Covers | Does not cover |
|---|---|---|
| The node durably records the highest version it accepted per deployment and refuses lower | Rollback on a node that ran a newer version | A freshly placed node, which has no record |
| A per-space chain of current deployment heads, one hash per user action, held by the nodes eligible for that space | Rollback on a fresh node | Freeze |
| Nodes exchange chain heads with each other | Freeze, unless the primary partitions every node consistently | |

Map acceptance has a version check today, but it is session-scoped: the first
map after a connect is accepted unconditionally (`networking.md`).

### What stays trusted

Placement among eligible nodes, routes in the net map, ACME state, and issued
workload TLS remain the primary's word. The primary can misroute traffic
between signed deployments on signed nodes. It cannot introduce a workload or a
node of its own.

### Enrolment

A compromised primary cannot enrol a node of its own, because the key wrap
needs a user. The remaining attack is substitution: during a real enrolment the
primary shows the operator its own key.

The enrolment identifier is the SHA-256 of the node's public key. If the
browser checks that the key it wraps to hashes to the identifier, the
operator's comparison of the identifier becomes the security check. The enrol
guide currently tells the operator to check the address.

## Prior art

### Tailscale tailnet lock

- Tailscale's coordination server distributes peers' public keys, so it could
  insert a node into a network.
- Each node has a separate lock key. Trusted nodes sign every new node's key.
  Peers verify the signature themselves and ignore unsigned nodes.
- The trusted key set is a chain of signed update messages, each referencing
  its parent's hash. The server stores and relays the chain but cannot forge a
  link.
- A trusted node can pre-sign an auth key, so unattended joins still work.
- `tailscale lock init` prints ten disablement secrets. Any one turns the lock
  off. Tailscale support holds one only on opt-in.
- It covers node membership only and encrypts nothing. Signing happens in the
  client binary, not the web console.
- Not confirmed from the documentation: access policy still comes from the
  server.

This is the node-membership part of layer 2. The split between an unverified
netmap and a fully synced key authority chain is the split used in
[Node verification](#node-verification).

### Keybase teams

- A per-user key is a key pair shared across a user's devices, with the public
  half in the user's signed chain. It rotates when a device is revoked, and
  each old seed is encrypted under the next generation.
- A per-team key is a seed with generations. Adding a member encrypts the seed
  to their per-user key. Removing a member rotates it for the remaining
  members.
- Membership and role changes are signed links in a team chain that clients
  verify, so the server cannot add a member.
- Rotation needs an online administrator, so the server signals administrators
  to rotate. The server could withhold the signal, so clients run a box
  auditor.
- An invite is a random token chosen by the administrator, with a derived value
  recorded in the team chain.
- Not confirmed: old data is never re-encrypted, and the global Merkle root was
  anchored to a public blockchain to detect diverging histories.

This is layer 3 without the primary half. The Keybase server only gates access
to ciphertext.

### Buildkite signed pipelines

The customer's signing agents hold a private key and sign pipeline steps. The
agents that run jobs hold the public key and verify each step before running
it. The keys are held by the customer's agents, not issued by the control
plane.

This is the static-key form of signed deployments in
[Managed service assessment](#managed-service-assessment).

### Mediated cryptography

The closest prior art for the primary half is mediated cryptography (Boneh and
others, 2001), where an online mediator holds a key share and revokes by
refusing. This reference is from memory and was not checked.

## Managed service assessment

This section assumes a managed service in which the vendor hosts the primary
and customers run the nodes. The market statements are general knowledge, not a
survey.

### Market position

- Most hosted control planes are fully trusted by their customers.
- Security reviews ask for single sign-on, role-based access, an exportable
  audit log, a compliance report, customer-managed encryption keys, tenant
  isolation, and data residency.
- A few vendors offer "the control plane cannot act on your machines" as an
  opt-in feature.

The full design is not needed to compete.

### Risks that grow with a hosted primary

- **Code execution on customer nodes.** A hosted primary that tells agents what
  to run is remote root on every customer's machines.
- **Customer secrets in the vendor's database.** A breach means every customer
  rotates every secret.

### Lower-cost forms

| Form | Replaces |
|---|---|
| The customer's CLI or CI signs each deployment version. The trusted public keys are written to the node at install and never come from the primary. The node refuses unsigned versions when enforcement is on | User keys, passkey derivation, and the authority chain |
| Two-key secrets with the customer's keys held outside the browser. The customer's CLI wraps the space private key to a node's key at approval. The primary keeps its outer half and its per-reference fetch check | Browser-held user keys and the authority chain |
| One primary per customer | Nothing; it limits a compromise to one customer |
| Session signatures (layer 1) | Nothing; it supplies the audit trail |

The two-key form without browser-held keys has three consequences:

- Reveal in the browser is unavailable, because the browser holds no key. It
  becomes a CLI operation.
- `PostV1SecretsGenerate` cannot run on the primary. The value is generated by
  the CLI or by an eligible node and encrypted to the space public key.
- The space keys need a recovery wrap, because the vendor cannot recover them.

### Central login

A managed service may give one person a single login across several clusters,
with a central identity provider that each cluster trusts. This section records
how that interacts with the keys in this design.

- **A session key belongs to one cluster.** Each cluster is its own browser
  origin and its own session row. The signed bytes include the cluster's
  identity, so a signature made for one cluster is not valid on another.
- **The login carries the session public key.** The cluster page generates the
  session key pair before it redirects to the identity provider, and the hash
  of the public key travels through the login as the nonce. The cluster binds
  the session row to that key when it verifies the signed login.
- **The identity provider authenticates and never admits a key.** A user key
  enters a cluster's authority chain only through a signature by a key already
  authorised in that cluster, or at genesis. Otherwise the identity provider is
  a trust root for every cluster.
- **User keys are per cluster.** One root secret per person derives a separate
  key pair for each cluster. A cluster's authority chain holds only its own
  public key for that person.
- **Recovering a login does not recover a key.** A password reset through the
  identity provider restores sign-in. Signing and reading secrets return only
  when another member admits the person's new key.

- **A session key lives and dies with its session row.** It has the row's
  expiry and is revoked by revoking the row. A new session needs a redirect
  through the identity provider, which is silent while the provider's own
  session is alive.
- **Signing out at the identity provider does not end cluster sessions.** The
  provider notifies each cluster the account belongs to, and the cluster
  revokes that account's session rows.
- **Nodes cannot see a revoked session.** Where nodes verify signatures, the
  session key carries a certificate from the user key with a short expiry, and
  that expiry bounds the exposure. Grants and node approvals are signed by the
  user key directly, not by a session key.

The static-key form above is unaffected, because the keys that nodes verify are
held by the CLI and never pass through the login.

### Browser-held keys under central login

Signing in and unlocking a key are separate steps. Single sign-on performs the
first. The second is a passkey operation that the cluster page performs
directly with the authenticator.

- **A passkey belongs to a domain, not to a server.** With the parent domain as
  its relying party, the identity provider stores the credential and verifies
  it at sign-in. A cluster page on a subdomain can also invoke the same passkey
  and evaluate its PRF. The PRF output reaches that page only.
- **One root secret per person.** The root is wrapped once per passkey and the
  wrapped copies are stored by the account service, which cannot open them. A
  cluster page fetches the wrapped root, unwraps it with the PRF output,
  derives the key pair for its own cluster, and discards the root. Adding a
  passkey is then one new wrap that covers every cluster.
- **Unlock is lazy.** The page asks for the passkey gesture at the first action
  in a session that needs the user key, not at sign-in.
- **A password alone cannot unlock a key.** The password is typed at the
  identity provider, so the cluster page never sees it. A person without a
  PRF-capable passkey uses the CLI or acts through session signatures only.
- **A third-party identity provider supplies no key material.** A person who
  signs in through a customer's own provider registers a passkey with the
  parent domain as a key holder, or uses the CLI.
- **Admission is separate from access to secrets.** An invite link can carry a
  secret whose public half the inviter signs into the authority chain, so the
  invited person's key is admitted when they redeem it. The space keys are
  wrapped to that key when a member's client is next online.

This needs the parent domain to stay off the Public Suffix List. It does not
extend to a customer's own domain without further browser support that was not
checked.

### External secret stores

An earlier position in the discussion replaced two-key secrets with secret
references that resolve on the node against the customer's own store. That
position is withdrawn as the default.

- The node needs a credential for the store. A compromised node reads
  everything that credential allows, which is wider than the references its
  deployments pin unless the customer scopes a credential per node.
- Scoping per node becomes the customer's work: roles, paths, and service
  account keys.
- The product aim is that one system owns secrets and their scoping. The
  primary's per-reference fetch check is that scoping, and the two-key design
  keeps it.

An external store remains an optional integration for a customer who is
required to use one.

## Recommended order

1. Build session signatures (layer 1). They need no model change, work with
   passwords, and put signatures into the write log, which is the costly part
   to retrofit.
2. Shape the formats so the rest is additive: a signature beside each write
   event mutation, and `user_key` and `node_key` variants in the keyslot
   `wrapping` union.
3. Build the TPM-sealed machine key (`secrets.md`, Phase 3). It is independent
   of this design.
4. For a managed service, build signed deployment versions with static keys on
   the node, hash pinning, durable version counters, and two-key secrets with
   the customer's keys held by the CLI and the nodes.
5. Defer browser-held user keys, the authority chain, and the per-space head
   chain until there is a concrete driver: a customer who separates the primary
   from workloads, a compliance requirement, or a need to sign or reveal from
   the browser under enforcement.

## Open questions

- Do nodes verify signatures? This decides between an audit feature and a
  change of trust model.
- Is a hosted primary planned? It is the strongest reason to build beyond
  layer 1.
- Does anything scope nodes to spaces today?
- Who resolves mutable references in a deployment spec (image tags, release
  names, branches)?
- Are entity ids chosen by the client or reserved before signing, so that a
  create signature can include its id?
- Is a signer's authority evaluated as of signing time, which needs authority
  history on the node and allows backdating by a removed user who colludes with
  the primary, or at the node's first acceptance of the version?
- How does a password user derive a key without sending the password?
- Does the primary run user workloads when the primary and workers are
  separated?
- Where does `PostV1SecretsGenerate` produce its value?

## Sources

- [Tailnet Lock](https://tailscale.com/docs/features/tailnet-lock)
- [Tailnet Lock white paper](https://tailscale.com/docs/concepts/tailnet-lock-whitepaper)
- [`tailscale lock` command](https://tailscale.com/docs/reference/tailscale-cli/lock)
- [Keybase teams crypto](https://book.keybase.io/docs/teams/crypto)
- [Keybase per-user keys](https://book.keybase.io/docs/teams/puk)
- [Keybase team sigchain details](https://book.keybase.io/docs/teams/details)
- [Keybase team box auditor](https://book.keybase.io/docs/teams/box-auditor)
- [Keybase Seitan invites](https://keybase.io/docs/seitan)
- [Buildkite signed pipelines](https://buildkite.com/docs/agent/v3/signed-pipelines)
