import assert from "node:assert/strict";
import test from "node:test";
import {deploymentHcl, deploymentHclLanguage} from "./index.js";

function errors(source) {
    const found = [];
    deploymentHclLanguage.parser.parse(source).iterate({
        enter(node) {
            if (node.type.isError) found.push({from: node.from, to: node.to});
        },
    });
    return found;
}

function nodeNames(source) {
    const names = [];
    deploymentHclLanguage.parser.parse(source).iterate({
        enter(node) { names.push(node.name); },
    });
    return names;
}

const fullConfig = `deployment {
  node = node("worker-1")
  name = "api"
  space = space("prod")

  container {
    source {
      nix_docker_build {
        repo = "github.com/acme/platform"
        flake = "services/api/flake.nix"
        target = ".#api-image"
      }
    }

    process {
      user = "svc"
      command = ["serve", "--port", "8080"]
      working_dir = "/srv"
    }

    env_vars = {
      "DB_PASS" = secret("db-pass", { version = 3 })
      "PEER" = address("prod", "worker")
      "MODE" = "production"
    }

    mounts = [
      mount(default_volume(), "/data"),
      mount(host_path("/var/cache"), "/cache", { read_only = true }),
      mount(asset("geoip", { version = 12 }), "/assets", { executable = true }),
    ]

    resources {
      dev_shm_size_kb = 65536
      file_descriptor_limit = 4096
    }

    upgrade {
      strategy = "rollover"
      readiness_timeout_seconds = 30
    }

    version = "v42"
  }

  network {
    mode = "virtual"

    ingress = [
      port_forward("tcp", 8080, { host_port = 80 }),
      https("api.example.test", 8080, { cert = acme(), path_prefix = "/v1", strip_prefix = true }),
      https("admin.example.test", 8443, { cert = secret("admin-cert", { version = 1 }) }),
      tls_passthrough("raw.example.test", 8443, { host_port = 443 }),
    ]
  }

  desired_running = true
}
`;

test("parses a full generated-style config without errors", () => {
    assert.deepEqual(errors(fullConfig), []);
});

test("parses function calls in object-value position", () => {
    for (const source of [
        'a = { b = acme() }',
        'env_vars = { "KEY" = secret("db-pass") }',
        'a = { b = secret("x", { version = 1 }) }',
        'a = { b = address("space", "deploy") }',
        'ingress = [{ https = acme() }]',
    ]) {
        assert.deepEqual(errors(source), [], source);
    }
});

test("parses zero-argument and nested calls", () => {
    assert.deepEqual(errors('cert = acme()'), []);
    assert.deepEqual(errors('m = mount(default_volume(), "/data")'), []);
    assert.deepEqual(errors('m = mount(host_path("/x"), "/y", { read_only = true })'), []);
});

test("parses trailing commas and optional object commas", () => {
    assert.deepEqual(errors('a = [1, 2, 3,]'), []);
    assert.deepEqual(errors('a = f("x",)'), []);
    assert.deepEqual(errors('a = { b = 1, c = 2 }'), []);
    assert.deepEqual(errors('a = { b = 1 c = 2 }'), []);
});

test("parses comments, negative numbers, and hyphenated identifiers", () => {
    assert.deepEqual(errors('# comment\n// comment\n/* multi\nline */\na = 1'), []);
    assert.deepEqual(errors('a = -5'), []);
    assert.deepEqual(errors('a = node("worker-1")'), []);
    assert.equal(nodeNames('a-b = 1')[2], "Identifier");
});

test("distinguishes booleans from identifiers", () => {
    const names = nodeNames("desired_running = true");
    assert.ok(names.includes("BoolLit"), names.join(" "));
    assert.ok(!nodeNames('a = truthy("x")').includes("BoolLit"));
});

test("produces distinct nodes for blocks, attributes, calls, and objects", () => {
    const names = nodeNames('b { a = f("x", { v = 1 }) }');
    for (const expected of ["Block", "BlockBody", "Attribute", "FunctionCall", "StringLit", "ObjectValue", "ObjectAttribute", "NumberLit"]) {
        assert.ok(names.includes(expected), `${expected} missing from ${names.join(" ")}`);
    }
});

test("flags genuinely invalid syntax", () => {
    for (const source of [
        'a = ',
        'block {',
        'a = f(',
        '= 3',
        'a = }',
        'a = "unterminated',
        'a = "no\nnewlines"',
        'a { b = 1',
    ]) {
        assert.ok(errors(source).length > 0, `expected errors for ${JSON.stringify(source)}`);
    }
});

test("exposes a CodeMirror language support", () => {
    const support = deploymentHcl();
    assert.equal(support.language, deploymentHclLanguage);
    assert.equal(deploymentHclLanguage.data.of({})[0], undefined);
});
