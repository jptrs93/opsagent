<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const installCommand = `curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_secondary.sh | bash -s -- \\
   --cluster-addr "opendeploy.example.com:9443" \\
   --enrollment-addr "opendeploy.example.com:9444" \\
   --enrollment-fingerprint "sha256:4f3a…" \\
   --version "v0.0.620"
`;

  const placement = `scheduling {
  running = true
  dedicated_nodes {
    nodes = [node("worker-1")]
  }
}
`;
</script>

<svelte:head>
  <title>Enrol a node — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Enrol a node</h1>
<p class="lead">Add a second machine to a cluster. You install the agent in secondary mode on the new machine, and accept it from the Cluster page of the primary.</p>

<!-- ============================================================ -->
<section id="prerequisites" class="page">
<h2>Prerequisites</h2>

<p>The new machine has the same requirements as the primary: Linux with systemd on amd64 or arm64, outbound internet access, and sudo. It must reach the primary on ports 9443 and 9444, and the two machines must reach each other on UDP port 51833, which carries the encrypted tunnel that cross-node traffic rides. Both machines need addresses of the same family, both IPv4 or both IPv6.</p>
</section>

<!-- ============================================================ -->
<section id="install" class="page">
<h2>Install the agent</h2>

<p>On the primary's Web UI open <strong>Nodes</strong>, then the <strong>Enrollments</strong> tab, and expand <strong>Install a secondary</strong>. The command shown there is filled in for your cluster: the primary's cluster and enrolment addresses, the fingerprint of the enrolment listener's certificate, and the agent version the primary runs. Copy it and run it on the new machine.</p>

<CodeBlock code={installCommand} language="text" />

<p>The installer makes the same changes to the machine as a primary install, described under <a href="{base}/guides/getting-started/#install-effects">What does the installation do?</a>, except that it creates no database and no master password. When the service starts, the agent generates a key pair, connects to the enrolment address, checks the fingerprint, and sends a certificate request. It then waits to be accepted, retrying if the primary is unreachable, so the order in which you do the next step does not matter.</p>

</section>

<!-- ============================================================ -->
<section id="accept" class="page">
<h2>Accept the request</h2>

<p>Back on the <strong>Enrollments</strong> tab the new machine appears under <strong>Pending requests</strong> with its remote address and agent version. Check that the address is the machine you just installed, type the name the node should have in the cluster, and click <strong>Accept</strong>.</p>

<p>Once accepted, the primary refuses a second enrolment from the same machine. A request that is not accepted within ten minutes expires, and the agent simply submits a new one.</p>

<p>The name is just a display label that can be changed at any time.</p>
</section>

<!-- ============================================================ -->
<section id="after" class="page">
<h2>After enrolment</h2>

<p>The node joins the <strong>Nodes</strong> tab within a few seconds, and two system deployments start on it, the agent and the network proxy. Its logs and metrics appear in the Web UI without further configuration.</p>

<p>To run a deployment on the new node, name it in the deployment's scheduling block. Deployments on different nodes reach each other by DNS name over the tunnel, as described in <a href="{base}/guides/connect-two-deployments/">Connect two deployments</a>.</p>

<CodeBlock code={placement} language="hcl" />
</section>

<!-- ============================================================ -->
<section id="remove" class="page">
<h2>Remove a node</h2>

<p>Select the node on the <strong>Nodes</strong> tab and click <strong>Evict</strong>. Eviction is permanent: the node is removed from the cluster, its containers and data are wiped, and its identifier stays listed so the same machine cannot rejoin by accident. Move or stop the deployments placed on it first, since they have nowhere to run once it is gone. To reuse the machine, uninstall and install the agent again, which gives it a new identity.</p>
</section>
