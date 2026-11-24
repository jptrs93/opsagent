<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import { base } from '$app/paths';
  import {
    minimalExample,
    minimalInstance,
        statementsExample,
    choiceExample,
    defaultsExample,
    diagnosticsExample,
    codeGrammar,
    typeExcerpt,
    grammar,
  } from '$lib/dsl-snippets';
</script>

<svelte:head>
  <title>Configuration code — OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Configuration code</h1>

<!-- ============================================================ -->
<section id="overview" class="page">
<h2>Overview</h2>

<p>All resources in OpenDeploy have a formalised underlying type definition. Instances can be created and updated via the control plane API where they are represented on the wire as JSON or protobuf. However, as a user, you create and edit resources in the Web UI, which has a built-in editor and a configuration code language that is more human friendly.</p>

<p>An example deployment written in configuration code looks like:</p>

<CodeBlock code={minimalExample} language="hcl" title="deployment" />

<p>When this code is saved, it is converted to an instance of the underlying type definition, and that instance is what is stored in the cluster state. For example, as JSON:</p>

<CodeBlock code={minimalInstance} language="json" title="stored instance" />

<p>A key difference is how resources refer to each other. In the code you write, you reference other resources by their display identifiers: the space is <code class="inl">space("prod")</code>, the secret is <code class="inl">secret("prod", "db/password")</code>, the node is <code class="inl">node("worker-1")</code>. On save, these are resolved to the underlying ids, and the ids are what the stored state contains: space 2, secret 11 at version 3, node 4.</p>

<p>The configuration code itself is never stored. It is always converted to, and back from, an instance of the underlying type. This keeps human-readable identifiers a surface-level concern that can be renamed trivially. The next time you open the configuration code it is converted again from the stored instance, and any reference shows the name the resource has now.</p>
</section>

<!-- ============================================================ -->
<section id="specification" class="page">
<h2>Configuration code specification</h2>

<p>Configuration code is a deliberately plain dialect of HCL. A document is a sequence of statements, and each statement is either an attribute or a block. There are no variables, no string interpolation, no arithmetic, and no block labels; everything a document needs to say is said by a value or a function call. Comments start with <code class="inl">#</code>.</p>

<h3>Statements</h3>

<p>The names of attributes and blocks are the field names of the resource's type. A field holding a single value is an attribute. A field holding a structure is a block. A field holding a list of structures is written as one block per item, repeated as often as needed.</p>

<CodeBlock code={statementsExample} language="hcl" />

<p>A document is one block named after the resource, so a deployment document is a single <code class="inl">deployment {'{'} … {'}'}</code> block. The <code class="inl">id</code> of a resource is never written; the cluster assigns it.</p>

<h3>Values</h3>

<table>
  <thead><tr><th>Value</th><th>Written as</th></tr></thead>
  <tbody>
    <tr><td>String</td><td><code class="inl">"text"</code>, with JSON escapes</td></tr>
    <tr><td>Number</td><td><code class="inl">8080</code>, <code class="inl">-1</code>, <code class="inl">2.5</code>; integers must fit the field's range</td></tr>
    <tr><td>Boolean</td><td><code class="inl">true</code> or <code class="inl">false</code></td></tr>
    <tr><td>Enumeration</td><td>The case name as a quoted string, such as <code class="inl">"Rollover"</code></td></tr>
    <tr><td>List</td><td><code class="inl">[a, b, c]</code></td></tr>
    <tr><td>Map or structure</td><td><code class="inl">{'{'} "KEY" = value {'}'}</code> as an object, or a block with the same entries</td></tr>
    <tr><td>Reference</td><td>A function call, such as <code class="inl">space("prod")</code></td></tr>
  </tbody>
</table>

<h3>Choices</h3>

<p>Some fields hold one of several alternatives: a workload source is a remote image or a Nix image build, an environment variable is a literal, a secret, a config value, an asset, or another deployment's address. A choice can be written in two ways.</p>

<CodeBlock code={choiceExample} language="hcl" />

<p>The value form works when the type of the value settles the alternative: each alternative that accepts a bare value has one field that carries it and nothing else that must be set. An alternative with more than one required field, such as an asset environment variable with its key and its asset, is written as a block or an object so that nothing is guessed. If a value could fit more than one alternative the editor says so and asks for the block form.</p>

<h3>Defaults</h3>

<p>A field that is left out takes its zero value: an empty string, zero, false, an empty list or map, the first case of an enumeration, or a structure with all of its fields zero. An optional field that is left out is absent. A field with no zero value, such as a reference or a choice, must be written, and the error names it: <code class="inl">Deployment requires space_id</code>.</p>

<CodeBlock code={defaultsExample} language="hcl" />

<p>When the editor opens a stored resource it prints only the fields that differ from their zero value, so the code you read back is as short as the code you wrote.</p>

<h3>Functions</h3>

<p>Functions are the only way to write a reference, and they are also used for a few values that have a natural name, such as <code class="inl">acme()</code> for a certificate source or <code class="inl">ipv4()</code> for the IPv4 any-address prefix. Each function has a fixed signature and a fixed return type, and a call is checked against both before it runs: a call with the wrong number of arguments or an argument of the wrong type never reaches the lookup.</p>

<p>Reference functions run against the cluster's state at the moment of saving. They find the named resource and return its id; a versioned one such as <code class="inl">secret</code> also pins the version, taking the newest unless the code names one. Some rules are enforced inside the function rather than by the type. A secret must live in the deployment's own space or the global space, and the function can see which space the document chose because that field is resolved first. A function that cannot resolve reports why, against the argument that was wrong.</p>

<p>The functions available in a deployment are listed under <a href="{base}/reference/deployment/#functions">functions</a> in the deployment reference.</p>

<h3>Errors</h3>

<p>Conversion walks the document in the order the type declares its fields, so at every point the expected type is known and an error can say exactly what was wanted. A string where a reference belongs is reported as needing <code class="inl">node(name)</code>, an unknown enumeration case lists the allowed cases, and a value of the wrong kind says what kind was expected. Rules that relate several fields, such as host networking forbidding ingress or a running deployment needing a version, run once the instance exists and report against the part of the document they concern. Nothing is stored unless the whole document converts without an error.</p>

<CodeBlock code={diagnosticsExample} language="text" />

<h3>Grammar</h3>

<CodeBlock code={codeGrammar} language="text" />
</section>

<!-- ============================================================ -->
<section id="types" class="page">
<h2>Type definition language</h2>

<p>Within these docs, the underlying type definitions are expressed in a simple custom type definition language, the specification of which is outlined below. A definition is a list of declarations. A <code class="inl">type</code> is a structure with named fields, and a type written in place of a field type is nested inside its parent. A <code class="inl">root</code> type is a stored resource with an <code class="inl">id</code>, and <code class="inl">Space.id</code> is a reference to one. An <code class="inl">enum</code> is a fixed set of cases, with the first as the default. Field types compose with <code class="inl">?T</code> for optional, <code class="inl">[]T</code> for a list, <code class="inl">[4]T</code> for a fixed-length array, <code class="inl">map&lt;K, V&gt;</code> for a map, and <code class="inl">A | B</code> for a choice.</p>

<CodeBlock code={typeExcerpt} language="typedef" />

<p>The names in configuration code follow the type: a field keeps its name, and a nested type or an alternative is named in snake case, so <code class="inl">RemoteImage</code> is written as <code class="inl">remote_image</code>.</p>

<CodeBlock code={grammar} language="text" />
</section>
