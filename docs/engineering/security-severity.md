# Security audit severity guidance

Methodology version: 1.

This document defines the severity scale for OpenDeploy security audits. It
distinguishes the damage an exploit causes from the access required to attempt
it, and provides consistent criteria for assessing and reporting findings.

## Industry conventions

Severity labels need an accompanying methodology. Two established approaches
serve different purposes:

- **CVSS** provides a standard technical severity score. Its Base score alone
  does not measure an organization's risk. Threat and Environmental metrics add
  exploitation and deployment context. See the
  [FIRST CVSS v4.0 User Guide](https://www.first.org/cvss/v4.0/user-guide#CVSS-Base-Score-CVSS-B-Measures-Severity-not-Risk).
- **Contextual risk assessment** combines likelihood and impact for a stated
  environment. OWASP's methodology considers attacker opportunity, population,
  exploit difficulty, technical consequences, and business consequences. It
  explicitly supports adapting the model to an organization. See the
  [OWASP Risk Rating Methodology](https://community.owasp.org/OWASP_Risk_Rating_Methodology).

CVSS v4.0 uses these bands:

| Label | Score |
| --- | --- |
| Critical | 9.0–10.0 |
| High | 7.0–8.9 |
| Medium | 4.0–6.9 |
| Low | 0.1–3.9 |
| None | 0.0 |

CVSS considers attack vector, required privileges, exploit complexity, attack
requirements, user interaction, and confidentiality, integrity, and availability
impacts. Its scoring does not reserve Critical for unauthenticated attacks.
Privileged access means the authority required before exploitation, not the
authority obtained afterward. Freely self-provisioned accounts generally do not
constitute a privilege requirement. See the
[FIRST CVSS v4.0 specification](https://www.first.org/cvss/v4.0/specification-document).

For OpenDeploy, use the contextual rating below as the report headline, explicitly
labelled **OpenDeploy severity**. If publishing a CVSS score as well, calculate it
separately and record the version, vector, and metric groups used. Do not translate
our qualitative matrix into a decimal CVSS score. Differences between the two
ratings need a short explanation.

## What each finding must distinguish

| Dimension | Question to answer |
| --- | --- |
| Starting access | What credentials, permissions, source control, workload execution, or network access must the attacker already possess? |
| Entry path | Which API, source repository, workload, node identity, or other input reaches the vulnerable behavior? |
| Exploit conditions | Is exploitation repeatable? Does it require another person's action, a race, an optional feature, or an independent compromise? |
| Impact | What new information, authority, destructive ability, or disruption does the attacker obtain? |
| Blast radius | Does the outcome affect one object, one space, one node, the primary, the cluster, or other systems? |
| Evidence | Which behavior was reproduced, which consequence follows from source review, and which step remains unverified? |
| Priority | Given this installation's exposure and current threat activity, what action should happen next? |

"Malicious insider" describes one possible actor. It does not fully specify the
prerequisite. An external attacker using a stolen deployment session has the same
starting authority as a malicious operator. A repository contributor can control
build input without having an OpenDeploy account. Record that authority directly;
do not assume account compromise has occurred or count a hypothetical credential
theft as a demonstrated unauthenticated attack.

Compare outcomes relative to the attacker's existing authority. A space operator
gaining host access crosses a boundary. A host administrator reading a file they
already control does not, by itself, demonstrate a new vulnerability.

## Baseline for OpenDeploy assessments

Unless a finding states otherwise, assess a privately administered cluster with
these assumptions:

- Cluster membership and deployment permissions are privately granted to known
  operators by an authorized administrator. Unknown internet users cannot grant
  themselves access.
- Scoped operators, agents, application code, and repository contributors are
  not automatically trusted with host or cluster administration.
- Cluster administrators and host administrators are trusted within their
  documented authority. Worker credentials do not imply primary administration.
- Public-facing interfaces are treated as reachable. Do not assume an unreported
  VPN, firewall, source approval process, or monitoring control mitigates a flaw.
- Feature-specific findings identify the affected configuration. Do not assume
  that every installation enables an optional feature or uses the same workflow.
- Actual customer counts, secret values, business losses, and production network
  exposure remain unknown unless verified.

These are assessment assumptions, not guarantees that the implementation enforces
every intended boundary. State deviations, particularly self-service membership,
untrusted tenants, automatic builds of external contributions, and primary-node
workload scheduling.

## OpenDeploy scale

Use the following impact and exploit-opportunity categories. They are qualitative
project definitions, not numerical probability estimates or CVSS metrics.

### Impact

| Impact | Definition and examples |
| --- | --- |
| Severe | Host or control-plane administrative authority; broad compromise of protected secrets; destructive loss of a cluster's workloads or data; or prolonged loss of essential cluster operations. A compromised worker qualifies as severe host impact, but cluster takeover needs its own supported path. |
| Material | Unauthorized access to sensitive resources or privileged operations with a bounded scope; compromise of another deployment's identity; or substantial disruption of a shared service. |
| Limited | Small disclosure of non-sensitive information, narrowly bounded unauthorized changes, or brief disruption with limited operational effect. |

Classify the most serious supported consequence. Availability-only findings can
have severe impact; "DoS" does not automatically mean Medium. Reading one signing
key can be more damaging than reading thousands of ordinary records. Explain the
authority the key conveys and any further material needed to use it.

### Exploit opportunity

| Opportunity | Definition |
| --- | --- |
| Broad | A repeatable path available to arbitrary internet attackers, freely admitted users, or ordinary untrusted inputs processed automatically. It also includes untrusted tenants admitted through routine customer onboarding who already hold the necessary permissions. No exceptional independent precondition or meaningful approval barrier limits exploitation. |
| Restricted | A feasible path requiring privately granted scoped access, control of a selected repository, an already compromised workload or worker, or verified restricted network access. Obtaining that starting position is a meaningful barrier, even if exploitation afterward is straightforward. |
| Exceptional | A path that additionally requires substantial privileged access, difficult exploit conditions, or a specific independent compromise. Name the concrete barrier and show that it limits this exploit. |

A login screen is insufficient evidence of restricted opportunity. Conversely,
remote API access does not establish that unknown users can exploit the API.
Record network reachability and authorization separately.

Missing reproduction is an evidence limitation, not proof that exploitation is
difficult. If a prerequisite is unknown, mark the rating provisional and give
conditional scenarios instead of assuming that the prerequisite is rare.

### Rating matrix

| Impact / Opportunity | Broad | Restricted | Exceptional |
| --- | --- | --- | --- |
| Severe | **Critical** | **High** | **High** |
| Material | **High** | **High** | **Medium** |
| Limited | **Medium** | **Low** | **Low** |

**Critical** means severe compromise with broad exploit opportunity. The
reference case is repeatable takeover by an unknown internet attacker. An
authenticated escape available to untrusted tenants can also qualify.

**High** means severe compromise behind a meaningful access barrier, or material
harm through a broadly available or feasible restricted path. Private deployment
authority escalating to host authority belongs here under the baseline.

**Medium** means material harm with exceptional prerequisites, or limited harm
through a broadly exploitable path.

**Low** means limited harm behind additional barriers.

**Informational** records a hardening opportunity or trust-model observation
without an established exploit that adds unauthorized impact. It is outside the
matrix and is not a synonym for an uninvestigated vulnerability.

The Severe/Exceptional cell deliberately remains High: a demonstrated host or
control-plane boundary failure deserves substantial attention even where access
is difficult. This is an OpenDeploy policy choice. Authentication alone neither
caps severity nor establishes that a flaw is exploitable.

## Classification examples

These hypothetical scenarios illustrate the scale. They do not describe known
vulnerabilities or imply that a particular installation exposes these paths.

| Scenario | Opportunity and impact | Rating |
| --- | --- | --- |
| An unknown internet attacker can repeatedly exploit a public endpoint to gain control-plane administrative authority. | Broad; severe control-plane compromise. | **Critical** |
| A freely admitted tenant can escape workload isolation and gain host administrative authority. | Broad; severe host compromise despite the requirement for an account. | **Critical** |
| A privately authorized space operator can escalate ordinary deployment permissions to host administrative authority. | Restricted; severe host compromise. | **High** |
| A privately authorized operator can obtain another deployment's service identity without gaining wider administrative authority. | Restricted; material unauthorized access. | **High** |
| An unknown internet attacker can cause brief, bounded service disruption without persistent damage. | Broad; limited availability impact. | **Medium** |
| A scoped user can read a small amount of non-sensitive metadata outside their assigned space. | Restricted; limited disclosure. | **Low** |
| A host administrator can intentionally modify resources already within their documented authority. | No additional unauthorized impact. | **Informational**, if useful to document |

Record blast radius even where the label stays the same. Compromising a worker
and compromising the primary may both be High behind a meaningful access
barrier, while requiring different containment and recovery actions. Do not
infer cluster takeover from worker compromise without a supported path.

## Intentional privileged access

Assess each finding against the documented permission and trust model for the
reviewed version. State what authority an exploit adds beyond that model.

A user bypassing a required privileged permission crosses a security boundary.
An administrator intentionally granting that permission makes a trust decision;
the documented authority it conveys is not itself a new vulnerability. Any
restrictions promised within that authority must still hold, including for a
privileged caller. Identify administrator responsibilities and verify that the
assessed environment meets them before treating them as mitigations.

Record the old vulnerable behavior, the implemented controls, their verification,
and the remaining assumptions separately. Do not silently replace the historical
finding with a lower severity or mark it closed solely because a policy changed.

## Evidence, priority, and reporting

Keep evidence labels distinct from severity:

- **Reproduced:** state the exact observed behavior and test environment. A
  validator accepting a path is not a reproduced host compromise.
- **Source verified:** the relevant execution or authorization path is traced;
  list runtime assumptions and any untested downstream consequences.
- **Unconfirmed:** a plausible path has unresolved steps. Give a provisional
  rating and identify the verification needed.

Priority also remains distinct. An actively exploited High finding can require
action ahead of a Critical finding in a disabled feature. Verified exposure,
asset importance, and effective mitigations should inform that choice; this is
consistent with the
[FIRST CVSS Consumer Implementation Guide](https://www.first.org/cvss/v4.0/implementation-guide).
Critical calls for immediate triage and containment; High for
prompt remediation planning; Medium for scheduled remediation; Low for ordinary
hardening work. Specific deadlines require an operational policy and are not
implied by these labels.

Each finding should include this compact record:

```text
Finding ID and affected revision/configuration:
OpenDeploy severity and methodology version/date:
Starting access and entry path:
Exploit conditions and opportunity category:
Impact category and blast radius:
Rating rationale, including alternative exposure scenarios:
Evidence and unresolved steps:
Optional CVSS version, score, vector, and metric groups:
Remediation priority and reason:
Status, mitigation/fix, and closure evidence:
```

Preserve dated assessment history when changing ratings. Identify whether a
change reflects a revised methodology, new evidence, a different deployment
environment, or a verified fix. These are different reasons for change.
