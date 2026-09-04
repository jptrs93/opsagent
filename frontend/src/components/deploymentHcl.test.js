import assert from "node:assert/strict";
import {test} from "node:test";
import {deploymentDocumentToHcl, parseDeploymentHcl} from "./deploymentHcl.js";

const catalogs = {
    nodes: [{id: 1, name: "primary"}, {id: 2, name: "worker-2"}],
    spaces: [{id: 1, name: "global"}],
    secretRefs: [{id: 7, name: "web-cert", version: 3, spaceId: 1}],
};

function document(networking) {
    return {
        identity: {name: "echo", spaceId: 1},
        nodeId: 2,
        spec: {
            container1Spec: {
                source: {remoteImage: {image: "docker.io/library/nginx"}},
                runtime: {defaultVolume: {disabled: true}},
                version: "1.27",
                running: true,
                upgradeStrategy: 1,
            },
            networking,
        },
    };
}

function roundTrip(networking) {
    const text = deploymentDocumentToHcl(document(networking), catalogs);
    const {document: parsed, diagnostics} = parseDeploymentHcl(text, catalogs);
    assert.deepEqual(diagnostics, [], text);
    return {text, networking: parsed.spec.networking};
}

test("renders and parses every ingress block kind with listen selectors", () => {
    const networking = {
        mode: 1,
        portForwarding: [
            {protocol: 1, hostPort: 5432, containerPort: 5432, ipFilter: {allow: ["198.51.100.0/24"]}},
            {protocol: 2, hostPort: 6000, containerPort: 5000},
        ],
        ingress: [
            {
                kind: 2,
                hostname: "api.example.test",
                httpsConfig: {containerPort: 5001, flushIntervalMs: -1, certSource: {acme: {}}},
                listen: [{node: {nodeId: 2}, address: {prefixes: ["203.0.113.10"]}}],
            },
            {
                kind: 2,
                hostname: "app.example.test",
                httpsConfig: {
                    containerPort: 5000, pathPrefix: "/api", stripPrefix: true, backendProtocol: 1,
                    maxRequestBodyBytes: 20000000, certSource: {secret: {secretVersionId: 7}},
                },
                listen: [{address: {family: 1}}, {node: {any: true}, address: {prefixes: ["2001:db8::10", "203.0.113.0/24"]}}],
            },
            {
                kind: 1,
                hostname: "db.example.test",
                tlsPassthroughConfig: {hostPort: 5433, containerPort: 5432},
                listen: [{address: {family: 2}}],
            },
            {kind: 1, hostname: "raw.example.test", tlsPassthroughConfig: {hostPort: 0, containerPort: 8443}},
        ],
    };
    const {text, networking: parsed} = roundTrip(networking);
    assert.match(text, /ingress \{\n {6}port_forward \{\n {8}protocol = "tcp"\n {8}container_port = 5432\n {8}allow = \["198\.51\.100\.0\/24"\]\n {6}\}/);
    assert.match(text, /https \{\n {8}hostname = "api\.example\.test"\n {8}container_port = 5001\n {8}flush_interval_ms = -1\n {8}cert = acme\(\)\n {8}listen \{\n {10}node = node\("worker-2"\)\n {10}address = "203\.0\.113\.10"\n {8}\}/);
    assert.match(text, /address = ipv4\(\)/);
    assert.match(text, /node = any_node\(\)\n {10}address = \["2001:db8::10", "203\.0\.113\.0\/24"\]/);
    assert.match(text, /tls_passthrough \{\n {8}hostname = "db\.example\.test"\n {8}container_port = 5432\n {8}host_port = 5433\n {8}listen \{\n {10}address = ipv6\(\)/);
    assert.doesNotMatch(text, /host_port = 8443/, "the default passthrough host port is omitted");
    assert.doesNotMatch(text, /https\(|tls_passthrough\(|port_forward\(/, "the call form is gone");
    assert.deepEqual(parsed, networking);
});

test("omits the ingress block when nothing is published and defaults host ports", () => {
    const {text, networking} = roundTrip({mode: 1});
    assert.doesNotMatch(text, /ingress/);
    assert.deepEqual(networking, {mode: 1});
    const {networking: forwarded} = roundTrip({mode: 1, portForwarding: [{protocol: 1, hostPort: 8080, containerPort: 8080}]});
    assert.deepEqual(forwarded, {mode: 1, portForwarding: [{protocol: 1, hostPort: 8080, containerPort: 8080}]});
});

test("listen block defaults to the scheduled node and any address", () => {
    const source = deploymentDocumentToHcl(document({mode: 1}), catalogs).replace('mode = "virtual"', `mode = "virtual"
    ingress {
      https {
        hostname = "api.example.test"
        container_port = 8080
        listen {}
        listen { node = scheduled_node() }
        listen { node = any_node() }
        listen { address = any_address() }
      }
    }`);
    const {document: parsed, diagnostics} = parseDeploymentHcl(source, catalogs);
    assert.deepEqual(diagnostics, []);
    assert.deepEqual(parsed.spec.networking.ingress[0].listen, [{}, {}, {node: {any: true}}, {address: {}}]);
});

test("rejects host_port on https, the call form, and bad selectors", () => {
    const base = deploymentDocumentToHcl(document({mode: 1}), catalogs);
    const cases = [
        [`ingress {\n      https {\n        hostname = "a.example"\n        container_port = 80\n        host_port = 8443\n      }\n    }`,
            /https is always published on 443; host_port is not configurable/],
        ['ingress = [https("a.example", 80)]', /Ingress routes are declared as blocks/],
        [`ingress {\n      https {\n        hostname = "a.example"\n        container_port = 80\n        listen { node = "primary" }\n      }\n    }`,
            /listen node must be scheduled_node\(\), node\("name"\), or any_node\(\)/],
        [`ingress {\n      https {\n        hostname = "a.example"\n        container_port = 80\n        listen { node = node("nope") }\n      }\n    }`,
            /No node named "nope" exists/],
        [`ingress {\n      https {\n        hostname = "a.example"\n        container_port = 80\n        listen { node = node("primary") }\n      }\n    }`,
            /Ingress is served by the deployment's own node/],
        [`ingress {\n      https {\n        hostname = "a.example"\n        container_port = 80\n        listen { address = "example.com" }\n      }\n    }`,
            /listen address entries must be quoted IP addresses or CIDR prefixes/],
        [`ingress {\n      tls_passthrough {\n        container_port = 80\n      }\n    }`, /Required attribute hostname is missing/],
        [`ingress {\n      port_forward {\n        protocol = "sctp"\n        container_port = 80\n      }\n    }`, /Port-forward protocol must be "tcp" or "udp"/],
        [`ingress {\n      https {\n        hostname = "a.example"\n        container_port = 80\n      }\n      https {\n        hostname = "b.example"\n        container_port = 80\n      }\n    }`, null],
    ];
    for (const [snippet, expected] of cases) {
        const source = base.replace('mode = "virtual"', `mode = "virtual"\n    ${snippet}`);
        const {diagnostics} = parseDeploymentHcl(source, catalogs);
        if (expected === null) {
            assert.deepEqual(diagnostics, [], snippet);
            continue;
        }
        assert.ok(diagnostics.some(item => expected.test(item.message)), `${snippet}\n${JSON.stringify(diagnostics)}`);
    }
});

test("renders identity, source version, and scheduling in their blocks and round-trips both source kinds", () => {
    const nix = document({mode: 2});
    nix.spec.container1Spec.source = {nixDockerBuild: {repo: "github.com/acme/app", flake: "app/flake.nix"}};
    nix.spec.container1Spec.version = "fb22005268a6fbf0f66008e52887c9b423646b57";
    const nixText = deploymentDocumentToHcl(nix, catalogs);
    assert.match(nixText, /^deployment \{\n {2}name = "echo"\n {2}space = space\("global"\)\n\n {2}container \{/);
    assert.match(nixText, /nix_docker_build \{\n {8}repo = "github\.com\/acme\/app"\n {8}flake = "app\/flake\.nix"\n {8}version = "fb22005268a6fbf0f66008e52887c9b423646b57"\n {6}\}/);
    assert.match(nixText, /network \{\n {4}mode = "host"\n {2}\}\n\n {2}scheduling \{\n {4}node = node\("worker-2"\)\n {4}desired_running = true\n {2}\}\n\}\n$/);
    assert.doesNotMatch(nixText, /\n {2}node = |\n {2}desired_running = |\n {4}version = /);
    const nixParsed = parseDeploymentHcl(nixText, catalogs);
    assert.deepEqual(nixParsed.diagnostics, []);
    assert.deepEqual(nixParsed.document, nix);

    const image = document({mode: 2});
    const imageText = deploymentDocumentToHcl(image, catalogs);
    assert.match(imageText, /container_image \{\n {8}image = "docker\.io\/library\/nginx:1\.27"\n {6}\}/);
    assert.doesNotMatch(imageText, /version = /);
    const imageParsed = parseDeploymentHcl(imageText, catalogs);
    assert.deepEqual(imageParsed.diagnostics, []);
    assert.deepEqual(imageParsed.document, image);
});

test("points the previous root node, desired_running, and container version at their new blocks", () => {
    const text = deploymentDocumentToHcl(document({mode: 2}), catalogs)
        .replace(/\n {2}scheduling \{[^}]*\}\n/, "\n  node = node(\"worker-2\")\n  desired_running = true\n")
        .replace(/\n {2}\}\n\n {2}network/, "\n    version = \"1.27\"\n  }\n\n  network");
    const {document: parsed, diagnostics} = parseDeploymentHcl(text, catalogs);
    assert.equal(parsed, null);
    const messages = diagnostics.map(item => item.message);
    assert.equal(messages.filter(message => /live in the scheduling block/.test(message)).length, 2, messages.join("\n"));
    assert.equal(messages.filter(message => /version is declared inside the source block/.test(message)).length, 1, messages.join("\n"));
    assert.ok(!messages.some(message => /is not valid in/.test(message)), messages.join("\n"));
});

const folderCatalogs = {
    ...catalogs,
    spaces: [{id: 1, name: "global"}, {id: 4, name: "prod"}],
    valueDirectories: [
        {id: 10, name: "ovh", parentId: 0, spaceId: 1},
        {id: 11, name: "cloud", parentId: 10, spaceId: 1},
        {id: 12, name: "certs", parentId: 0, spaceId: 4},
    ],
    assetDirectories: [{id: 20, key: "geo", parentId: 0, spaceId: 1}],
    secretRefs: [
        {id: 30, name: "user1.secret", version: 1, spaceId: 1, directoryId: 11},
        {id: 31, name: "user1.secret", version: 2, spaceId: 1, directoryId: 11},
        {id: 32, name: "user1.secret", version: 1, spaceId: 1, directoryId: 0},
        {id: 33, name: "web-cert", version: 1, spaceId: 4, directoryId: 12},
        {id: 34, name: "other", version: 1, spaceId: 5, directoryId: 0},
    ],
    configRefs: [{id: 40, name: "access_key_id", version: 3, spaceId: 1, directoryId: 10}],
    assets: [{id: 50, key: "db", spaceId: 1, directoryId: 20, contentVersions: [{id: 51, version: 1}, {id: 52, version: 2}]}],
};

function prodDocument(runtime, ingress = []) {
    const doc = document({mode: 1, ingress});
    doc.identity.spaceId = 4;
    doc.spec.container1Spec.runtime = {defaultVolume: {disabled: true}, ...runtime};
    return doc;
}

test("references carry the space, the folder path, and a positional version", () => {
    const doc = prodDocument({
        envVars: {
            KEY: {configVersionId: 40},
            SECRET: {secretVersionId: 31},
            ROOT_SECRET: {secretVersionId: 32},
            DB: {asset: "db", assetVersionId: 51},
        },
        assetMounts: [{assetVersionId: 52, containerPath: "/geo", permission: 2}],
    }, [{kind: 2, hostname: "web.example.test", httpsConfig: {containerPort: 80, certSource: {secret: {secretVersionId: 33}}}}]);
    const text = deploymentDocumentToHcl(doc, folderCatalogs, {pinVersions: true});
    assert.match(text, /"KEY" = config\("global", "ovh\/access_key_id", 3\)/);
    assert.match(text, /"SECRET" = secret\("global", "ovh\/cloud\/user1\.secret", 2\)/);
    assert.match(text, /"ROOT_SECRET" = secret\("global", "user1\.secret", 1\)/);
    assert.match(text, /"DB" = asset\("global", "geo\/db", 1\)/);
    assert.match(text, /mount\(asset\("global", "geo\/db", 2\), "\/geo"\)/);
    assert.match(text, /cert = secret\("prod", "certs\/web-cert", 1\)/);
    assert.doesNotMatch(text, /\{ (version|space) = /);
    const {document: parsed, diagnostics} = parseDeploymentHcl(text, folderCatalogs);
    assert.deepEqual(diagnostics, []);
    assert.deepEqual(parsed, doc);

    // Unpinned create-mode output omits the latest version only.
    const unpinned = deploymentDocumentToHcl(doc, folderCatalogs);
    assert.match(unpinned, /"SECRET" = secret\("global", "ovh\/cloud\/user1\.secret"\)/);
    assert.match(unpinned, /"DB" = asset\("global", "geo\/db", 1\)/);
    const latest = parseDeploymentHcl(unpinned, folderCatalogs);
    assert.deepEqual(latest.diagnostics, []);
    assert.equal(latest.document.spec.container1Spec.runtime.envVars.SECRET.secretVersionId, 31);
});

test("rejects the old options form, foreign spaces, unknown paths, and bad versions", () => {
    const base = deploymentDocumentToHcl(prodDocument({}), folderCatalogs);
    const withEnv = value => base.replace(/\n {2}container \{\n/, `\n  container {\n    env_vars = {\n      "X" = ${value}\n    }\n`);
    const cases = [
        ['secret("user1.secret", { version = 1 })', /Secret references are secret\("space", "folder\/name"\[, version\]\); the \{ version, space \} options object is gone/],
        ['secret("user1.secret")', /options object is gone/],
        ['secret("staging", "other", 1)', /may only use the deployment's space or the global space/],
        ['secret("global", "cloud/user1.secret")', /No secret at "cloud\/user1\.secret" exists in space "global"/],
        ['secret("global", "ovh/cloud/user1.secret", 9)', /No secret at "ovh\/cloud\/user1\.secret" version 9 exists in space "global"/],
        ['secret("global", "ovh/cloud/user1.secret", 0)', /Secret version must be a positive integer/],
        ['asset("global", "db", 1)', /No asset at "db" version 1 exists in space "global"/],
    ];
    const withStaging = {...folderCatalogs, spaces: [...folderCatalogs.spaces, {id: 5, name: "staging"}]};
    for (const [value, expected] of cases) {
        const {document: parsed, diagnostics} = parseDeploymentHcl(withEnv(value), withStaging);
        assert.equal(parsed, null, value);
        assert.ok(diagnostics.some(item => expected.test(item.message)), `${value}: ${diagnostics.map(item => item.message).join("\n")}`);
    }
});

test("container images carry their version as the reference tag or digest", () => {
    const render = (image, version, running = true) => {
        const doc = document({mode: 2});
        doc.spec.container1Spec.source = {remoteImage: {image}};
        doc.spec.container1Spec.version = version;
        doc.spec.container1Spec.running = running;
        return {doc, text: deploymentDocumentToHcl(doc, catalogs)};
    };
    // A stored reference that still carries a tag renders the version once.
    const tagged = render("ghcr.io/jptrs93/declaritive-postgres:18.4_v12", "18.4_v12");
    assert.match(tagged.text, /image = "ghcr\.io\/jptrs93\/declaritive-postgres:18\.4_v12"/);
    const taggedParsed = parseDeploymentHcl(tagged.text, catalogs);
    assert.deepEqual(taggedParsed.diagnostics, []);
    assert.equal(taggedParsed.document.spec.container1Spec.source.remoteImage.image, "ghcr.io/jptrs93/declaritive-postgres");
    assert.equal(taggedParsed.document.spec.container1Spec.version, "18.4_v12");

    const digest = render("docker.io/library/postgres", "sha256:0123456789abcdef");
    assert.match(digest.text, /image = "docker\.io\/library\/postgres@sha256:0123456789abcdef"/);
    const digestParsed = parseDeploymentHcl(digest.text, catalogs);
    assert.deepEqual(digestParsed.diagnostics, []);
    assert.deepEqual(digestParsed.document, digest.doc);

    const port = render("localhost:5000/app", "1.2");
    assert.match(port.text, /image = "localhost:5000\/app:1\.2"/);
    const portParsed = parseDeploymentHcl(port.text, catalogs);
    assert.deepEqual(portParsed.diagnostics, []);
    assert.deepEqual(portParsed.document, port.doc);

    // No tag: fine for a stopped deployment, a diagnostic for a running one.
    const stopped = render("localhost:5000/app", "", false);
    assert.match(stopped.text, /image = "localhost:5000\/app"\n/);
    const stoppedParsed = parseDeploymentHcl(stopped.text, catalogs);
    assert.deepEqual(stoppedParsed.diagnostics, []);
    assert.equal(stoppedParsed.document.spec.container1Spec.version, "");
    const running = parseDeploymentHcl(stopped.text.replace("desired_running = false", "desired_running = true"), catalogs);
    assert.equal(running.document, null);
    assert.ok(running.diagnostics.some(item => /must include a tag or digest/.test(item.message)), running.diagnostics.map(item => item.message).join("\n"));

    // The previous separate version attribute earns the hint only.
    const old = tagged.text.replace(/image = "([^"]*):18\.4_v12"/, 'image = "$1"\n        version = "18.4_v12"');
    const oldParsed = parseDeploymentHcl(old, catalogs);
    assert.equal(oldParsed.document, null);
    const messages = oldParsed.diagnostics.map(item => item.message);
    assert.equal(messages.filter(message => /versioned by its reference/.test(message)).length, 1, messages.join("\n"));
    assert.ok(!messages.some(message => /is not valid in/.test(message)), messages.join("\n"));
});
