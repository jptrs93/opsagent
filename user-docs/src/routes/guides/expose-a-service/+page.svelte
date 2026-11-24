<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';

  const httpsRoute = `network {
  ingress {
    https {
      hostname       = "api.example.com"
      container_port = 8080
    }
  }
}
`;

  const ownCertificate = `https {
  hostname       = "api.example.com"
  container_port = 8080
  cert           = secret("prod", "tls/api-example-com")
}
`;

  const passthrough = `network {
  ingress {
    tls_passthrough {
      hostname       = "mail.example.com"
      container_port = 8443
    }
  }
}
`;

  const portForward = `network {
  ingress {
    port_forward {
      protocol       = "tcp"
      container_port = 5432
      host_port      = 5432
      allow          = ["203.0.113.0/24"]
    }
  }
}
`;

  const listen = `https {
  hostname       = "api.example.com"
  container_port = 8080
  listen {
    node    = any_node()
    address = ipv4()
  }
}
`;
</script>

<svelte:head>
  <title>Expose a service — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Expose a service</h1>
<p class="lead">Make a deployment reachable from outside the cluster. Everything here is declared in the deployment's network block; nothing is configured on the node.</p>

<!-- ============================================================ -->
<section id="choose" class="page">
<h2>Choose by the traffic</h2>

<p>A container has no ports open to the outside until its deployment publishes a route. There are three kinds, and which one you use depends on what the traffic is, not on where it should land.</p>

<table>
  <thead><tr><th>Route</th><th>Use it for</th><th>What happens</th></tr></thead>
  <tbody>
    <tr><td><code class="inl">https</code></td><td>HTTP services</td><td>The node's built-in proxy terminates TLS with a certificate it obtains and renews for you, routes by hostname and path, and redirects plain HTTP to HTTPS.</td></tr>
    <tr><td><code class="inl">tls_passthrough</code></td><td>Services that bring their own TLS</td><td>The proxy reads the hostname from the handshake and relays the encrypted stream to the container untouched.</td></tr>
    <tr><td><code class="inl">port_forward</code></td><td>Anything else: databases, game servers, UDP</td><td>A kernel rule maps a host port straight to the container, optionally limited to an allow list of sources.</td></tr>
  </tbody>
</table>

<p>The first two share port 443 on the node, so any number of hostnames can be published side by side. A port forward owns its host port.</p>
</section>

<!-- ============================================================ -->
<section id="https" class="page">
<h2>HTTPS with an automatic certificate</h2>

<p>Point a DNS record for the hostname at the node's public address, and make sure ports 80 and 443 reach the node. Then add the route to the deployment and save it.</p>

<CodeBlock code={httpsRoute} language="hcl" />

<p>As soon as the route is saved, the primary requests a certificate for the hostname from Let's Encrypt and renews it before it expires. Until it is issued, usually within a minute, connections to the hostname fail their TLS handshake. From then on <code class="inl">https://api.example.com</code> reaches port 8080 in the container, and <code class="inl">http://api.example.com</code> redirects there.</p>

<p>Several deployments can share a hostname by claiming different path prefixes, and the longest prefix wins. Add <code class="inl">path_prefix = "/admin"</code> to a route, with <code class="inl">strip_prefix = true</code> if the container expects paths without it.</p>

<p>If your organisation issues certificates, store the PEM certificate and key as a secret and name it in the route instead. The route then serves that certificate as given, and you replace the secret when a new one is issued.</p>

<CodeBlock code={ownCertificate} language="hcl" />
</section>

<!-- ============================================================ -->
<section id="passthrough" class="page">
<h2>TLS passthrough</h2>

<p>When the container terminates TLS itself, publish it by hostname and let the proxy relay the raw stream. The container's certificate is its own business; the proxy never decrypts.</p>

<CodeBlock code={passthrough} language="hcl" />

<p>The route listens on port 443 by default and can share it with HTTPS routes for other hostnames. Set <code class="inl">host_port</code> to publish on another port.</p>
</section>

<!-- ============================================================ -->
<section id="port-forward" class="page">
<h2>Raw ports</h2>

<p>For a protocol that is not TLS, forward a host port to the container. Traffic goes straight through the kernel with no proxy in the path.</p>

<CodeBlock code={portForward} language="hcl" />

<p>The allow list is optional and takes addresses or prefixes. Without one the port is open to everyone. Sources outside the list see the port as closed. This is the mechanism the <a href="{base}/guides/getting-started/#first-deployment">first deployment</a> on Getting started uses.</p>
</section>

<!-- ============================================================ -->
<section id="where" class="page">
<h2>Where a route is published</h2>

<p>By default a route is published on every public address of the node the deployment runs on. An https or passthrough route can carry listen blocks to choose differently: which nodes publish it, and which of their addresses.</p>

<CodeBlock code={listen} language="hcl" />

<p><code class="inl">any_node()</code> publishes the route on every node in the cluster; the proxy on each node reaches the container over the internal network, so one public DNS record per node gives you more than one way in. The address selector takes <code class="inl">ipv4()</code>, <code class="inl">ipv6()</code>, or a literal address when a node has several.</p>

<p>On the primary, the Web UI already holds port 443 on its own address. A route that would land exactly there is rejected when you save; a route for every address simply skips it. Two deployments cannot publish the same hostname on the same node and port, even on different addresses: the proxy routes by hostname alone. The second save tells you so.</p>

<p>Field by field detail is in the <a href="{base}/reference/deployment/#ingress">network.ingress reference</a>, and the routing design is in <a href="{base}/design/networking/#ingress">Networking</a>.</p>
</section>
