import assert from "node:assert/strict";
import {test} from "node:test";
import {
    compileSchema,
    defineFunctions,
    deploymentSchema,
    deploymentTypeDefinition,
    evaluate,
    evaluateDeployment,
    parseCode,
    parseTypeDefinitions,
    print,
    printDeployment,
    snakeCase,
    validateInstance,
} from "./index.js";

const catalog = {
    spaces: [{id: 1, name: "global"}, {id: 2, name: "prod"}, {id: 3, name: "staging"}],
    nodes: [{id: 1, name: "primary"}, {id: 4, name: "worker-1"}],
    deployments: [{id: 7, name: "redis", spaceId: 2}, {id: 8, name: "redis", spaceId: 3}],
    secrets: [
        {id: 11, spaceId: 2, path: "db/password", version: 1},
        {id: 11, spaceId: 2, path: "db/password", version: 3},
        {id: 12, spaceId: 3, path: "db/password", version: 1},
        {id: 13, spaceId: 2, path: "tls/api", version: 1},
    ],
    configs: [{id: 20, spaceId: 1, path: "api/log-level", version: 1}, {id: 20, spaceId: 1, path: "api/log-level", version: 2}],
    assets: [{id: 31, spaceId: 2, path: "google/sa.json", version: 1}],
};

const example = `deployment {
  name     = "api"
  space_id = space("prod")

  spec {
    workload {
      source {
        remote_image {
          image = "ghcr.io/acme/api:1.4.2"
        }
      }

      runtime {
        env_vars = {
          "DATABASE_PASSWORD" = secret("prod", "db/password")
          "LOG_LEVEL"         = config("global", "api/log-level", 1)
          "REDIS_HOST"        = address("prod", "redis")
          "PORT"              = "8080"
        }

        default_volume {
          container_path = "/data"
        }

        asset_mounts {
          asset          = asset("prod", "google/sa.json")
          container_path = "/etc/sa.json"
          permission     = "ReadExecute"
        }

        issued_tls_mount {
          container_path = "/etc/tls"
          ca_only        = true
        }
      }

      upgrade_strategy = "Rollover"

      readiness_signal {
        timeout_seconds = 30
      }
    }

    networking {
      port_forwarding {
        host_port      = 5432
        container_port = 5432

        ip_filter {
          prefix = prefix("203.0.113.0/24")
        }
      }

      ingress {
        hostname = "api.example.com"

        listen {
          node      = any_node()
          addresses = [ipv4()]
        }

        config {
          https_config {
            container_port = 8080
            cert_source    = acme()
          }
        }
      }
    }
  }

  scheduling {
    running = true

    placement {
      nodes = [node("worker-1")]
    }
  }
}
`;

const expectedInstance = {
    name: "api",
    space_id: 2,
    spec: {
        workload: {
            source: {remote_image: {image: "ghcr.io/acme/api"}},
            runtime: {
                user: "",
                env_vars: {
                    DATABASE_PASSWORD: {secret_env: {secret: {secret_id: 11, version: 3}}},
                    LOG_LEVEL: {config_env: {config: {config_id: 20, version: 1}}},
                    REDIS_HOST: {address_env: {deployment_id: 7, space_id: 2}},
                    PORT: {literal_env: {value: "8080"}},
                },
                override_command: [],
                override_working_dir: "",
                default_volume: {container_path: "/data", disabled: false},
                cross_deployment_mounts: [],
                mounts: [],
                asset_mounts: [{asset: {asset_id: 31, version: 1}, container_path: "/etc/sa.json", permission: "ReadExecute"}],
                issued_tls_mount: {container_path: "/etc/tls", extra_names: [], ca_only: true},
            },
            version: "1.4.2",
            upgrade_strategy: "Rollover",
            readiness_signal: {timeout_seconds: 30},
        },
        networking: {
            mode: "Virtual",
            port_forwarding: [{
                protocol: "Tcp",
                host_port: 5432,
                container_port: 5432,
                ip_filter: [{mode: "Allow", prefix: {ipv4_prefix: {address: {octets: [203, 0, 113, 0]}, prefix_length: 24}}}],
            }],
            ingress: [{
                hostname: "api.example.com",
                listen: [{node: {any_node: {}}, addresses: [{ipv4_prefix: {address: {octets: [0, 0, 0, 0]}, prefix_length: 0}}]}],
                config: {
                    https_config: {
                        container_port: 8080,
                        path_prefix: "",
                        strip_prefix: false,
                        backend_protocol: "Http1",
                        cert_source: {acme_cert_source: {challenge: "Http01"}},
                    },
                },
            }],
        },
    },
    scheduling: {running: true, placement: {nodes: [4]}, restart_generation: 0},
};

const messages = result => result.diagnostics.map(item => item.message);
const errorAt = (text, result) => result.diagnostics.map(item => text.slice(item.from, item.to));

test("snakeCase follows the type names of the model", () => {
    assert.equal(snakeCase("IPv4Prefix"), "ipv4_prefix");
    assert.equal(snakeCase("IssuedTLSMount"), "issued_tls_mount");
    assert.equal(snakeCase("HttpsConfig"), "https_config");
    assert.equal(snakeCase("AnyNode"), "any_node");
});

test("the deployment type definition compiles into a nested schema", () => {
    const schema = deploymentSchema();
    const deployment = schema.roots.get("Deployment");
    assert.deepEqual(deployment.fields.map(field => field.name), ["id", "name", "space_id", "spec", "scheduling"]);
    assert.equal(deployment.fieldMap.get("space_id").type.kind, "key");
    assert.equal(deployment.fieldMap.get("space_id").type.root.name, "Space");
    const spec = deployment.nested.get("DeploymentSpec");
    const workload = spec.nested.get("ContainerSpec");
    assert.equal(workload.qualified, "Deployment.DeploymentSpec.ContainerSpec");
    const source = workload.fieldMap.get("source").type;
    assert.equal(source.kind, "union");
    assert.deepEqual(source.alternatives.map(alt => alt.codeName), ["nix_image_build", "remote_image"]);
    const envVars = workload.nested.get("ContainerRuntime").fieldMap.get("env_vars").type;
    assert.equal(envVars.kind, "map");
    assert.deepEqual(envVars.value.alternatives.map(alt => alt.codeName), ["literal_env", "secret_env", "config_env", "asset_env", "address_env"]);
    assert.equal(schema.types.get("IPv4Address").fieldMap.get("octets").type.kind, "array");
    assert.equal(schema.types.get("IPv4Address").fieldMap.get("octets").type.length, 4);
    assert.deepEqual(schema.types.get("Deployment.DeploymentSpec.NetworkingConfig.Ingress.HttpsConfig.HttpBackendProtocol").cases, ["Http1", "H2c"]);
});

test("the type parser accepts the e2edesign forms and refuses tags, laws, and metadata", () => {
    const ok = parseTypeDefinitions(`
        id Token = string
        enum Mode { A; B, C }
        type Thing root {
            id: u64
            kind: Mode
            parts: []type Part { name: string } ;
            grid: [3]f64,
            extra: ?map<string, Token | i32>
            other: (Part | Mode)
        }
    `);
    assert.deepEqual(ok.diagnostics, []);
    assert.deepEqual(ok.declarations.map(item => `${item.kind} ${item.name}`), ["id Token", "enum Mode", "record Thing"]);
    const failures = [
        ["type Thing@1 root { id: u64 }", /Tags are not part/],
        ["type Thing root { id@1: u64 }", /Tags are not part/],
        ["type Thing root { id: u64 [key] }", /metadata are not part/],
        ["type Thing root { id: u64\n laws { x { assert id > 0 } } }", /Laws are not part/],
        ["laws global { }", /Laws are not part/],
        ["enum Mode { reserve @0 A }", /Reserved tags are not part/],
        ["type Thing root { name: string } type Thing { }", /declared more than once/],
        ["type Thing root { ref: Other.id }", /Unknown type Other\.id/],
        ["type Other { id: u64 } type Thing root { ref: Other.id }", /not a root/],
        ["type Thing root { m: map<f64, string> }", /Map keys must be one of/],
        ["type Thing root { x: ??string }", /optional of an optional/],
        ["resource Thing { }", /Unknown declaration resource/],
    ];
    for (const [text, pattern] of failures) {
        const result = compileSchema(text);
        assert.equal(result.schema, null, text);
        assert.match(result.diagnostics[0].message, pattern, text);
    }
});

test("the code parser produces attributes, blocks, and nested expressions", () => {
    const {ast, diagnostics} = parseCode('a = "x"\nb { c = [1, 2.5, true, f("y", {k = 1})] }');
    assert.deepEqual(diagnostics, []);
    assert.equal(ast.body[0].kind, "attribute");
    assert.equal(ast.body[1].kind, "block");
    const list = ast.body[1].body[0].value;
    assert.deepEqual(list.items.map(item => item.kind), ["number", "number", "boolean", "call"]);
    assert.equal(list.items[1].value, 2.5);
    assert.equal(list.items[3].args[1].entries[0].name, "k");
    assert.match(parseCode('a "label" {}').diagnostics[0].message, /Block labels are not supported/);
});

test("evaluating the example yields a dense instance", () => {
    const result = evaluateDeployment(example, catalog);
    assert.deepEqual(messages(result), []);
    assert.deepEqual(result.instance, expectedInstance);
    assert.ok(result.nodeOf(result.instance.spec.workload.runtime.asset_mounts[0]));
    assert.equal(validateInstance(deploymentSchema(), "Deployment", result.instance).ok, true);
});

test("printing the instance is a fixed point of the example", () => {
    const printed = printDeployment(expectedInstance, catalog);
    assert.equal(printed, example);
    const again = evaluateDeployment(printed, catalog);
    assert.deepEqual(messages(again), []);
    assert.deepEqual(again.instance, expectedInstance);
});

test("printing uses current names and pins a version only when it is no longer newest", () => {
    const renamed = {
        ...catalog,
        deployments: [{id: 7, name: "cache", spaceId: 2}],
        secrets: [...catalog.secrets, {id: 11, spaceId: 2, path: "db/password", version: 4}],
    };
    const printed = printDeployment(expectedInstance, renamed);
    assert.match(printed, /"REDIS_HOST"\s+= address\("prod", "cache"\)/);
    assert.match(printed, /"DATABASE_PASSWORD" = secret\("prod", "db\/password", 3\)/);
    assert.match(printed, /"LOG_LEVEL"\s+= config\("global", "api\/log-level", 1\)/);
});

test("union alternatives are chosen by value type, and wrapped records print back inline", () => {
    const text = `deployment {
  name     = "edge"
  space_id = space("prod")

  spec {
    workload {
      source = "ghcr.io/acme/edge:2"

      runtime {
        env_vars = { "MODE" = "fast" }
      }
    }

    networking {
      ingress {
        hostname = "edge.example.com"

        listen {
          node      = node("worker-1")
          addresses = [prefix("2001:db8::/32"), ipv6()]
        }

        config {
          tls_passthrough_config {
            container_port = 443
          }
        }
      }

      ingress {
        hostname = "tls.example.com"

        config {
          https_config {
            container_port = 8443
            cert_source    = secret("prod", "tls/api")
          }
        }
      }
    }
  }

  scheduling {
    placement {
      nodes = [node("primary")]
    }
  }
}
`;
    const result = evaluateDeployment(text, catalog);
    assert.deepEqual(messages(result), []);
    const [first, second] = result.instance.spec.networking.ingress;
    assert.deepEqual(result.instance.spec.workload.source, {remote_image: {image: "ghcr.io/acme/edge"}});
    assert.equal(result.instance.spec.workload.version, "2");
    assert.deepEqual(first.listen[0].node, {specific_node: {node_id: 4}});
    assert.deepEqual(first.listen[0].addresses[0], {ipv6_prefix: {address: {octets: [0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]}, prefix_length: 32}});
    assert.deepEqual(first.listen[0].addresses[1].ipv6_prefix.prefix_length, 0);
    assert.deepEqual(first.config, {tls_passthrough_config: {container_port: 443}});
    assert.deepEqual(second.config.https_config.cert_source, {secret_cert_source: {secret: {secret_id: 13, version: 1}}});
    assert.equal(result.instance.scheduling.running, false);
    const printed = printDeployment(result.instance, catalog);
    assert.match(printed, /source \{\n\s+remote_image \{\n\s+image = "ghcr.io\/acme\/edge:2"/);
    assert.match(printed, /node\s+= node\("worker-1"\)/);
    assert.match(printed, /addresses = \[prefix\("2001:db8::\/32"\), ipv6\(\)\]/);
    assert.match(printed, /cert_source\s+= secret\("prod", "tls\/api"\)/);
    assert.match(printed, /listen \{\n\s+node\s+= node\("worker-1"\)/);
    assert.ok(!printed.includes("running"));
    assert.deepEqual(evaluateDeployment(printed, catalog).instance, result.instance);
});

test("structural errors name the field, the type, and the accepted forms", () => {
    const check = (body, pattern, highlight) => {
        const text = `deployment {\n  name = "x"\n  space_id = space("prod")\n${body}\n  scheduling { placement { nodes = [node("primary")] } }\n}`;
        const result = evaluateDeployment(text, catalog);
        assert.equal(result.instance, null, body);
        assert.match(messages(result)[0], pattern, body);
        if (highlight) assert.equal(errorAt(text, result)[0], highlight, body);
    };
    check('  id = 4', /id is assigned by the cluster/, "id");
    check('  colour = "red"', /Deployment has no field named colour/, "colour");
    check('  spec { workload { source { } } }', /source requires one of nix_image_build, remote_image/);
    check('  spec { workload { source { remote_image { image = "a" } nix_image_build { repo = "r" } } } }', /source allows only one of/);
    check('  spec { workload { source { remote_image { image = "a" } } version = "3" } }', /version is written inside the source block/, "version");
    check('  spec { workload { source { remote_image { image = "a" version = "3" } } } }', /versioned by its reference/, "version");
    check('  spec { workload { source { remote_image { image = "a" } } upgrade_strategy = "rolling" } }', /"rolling" is not one of "Recreate", "Rollover"/);
    check('  spec { workload { source { remote_image { image = "a" } } } networking { mode = "Host" ingress { hostname = "h" config { tls_passthrough_config { container_port = 1 } } } } }', /Host networking cannot declare ingress/);
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars = { "A" = 5 } } } }', /The value fits .*|Expected/);
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars = { "A" = { key = "k" } } } } }', /An object is ambiguous here/);
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars { "A" = "1" "A" = "2" } } } }', /declared more than once/);
    check('  spec { workload { source { remote_image { image = "a" } } runtime { user { } } } }', /user is an attribute: user = a string/, "user");
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars = { "A" = address("prod", "redis") } } } networking { port_forwarding { protocol = "Tcp" host_port = 70000 container_port = 80 } } }', /host_port must be between 1 and 65535/);
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars = { "A" = secret("staging", "db/password") } } } }', /Secret references may only use the deployment's space or the global space/, '"staging"');
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars = { "A" = secret("prod", "db/password", 2) } } } }', /No secret at "db\/password" version 2 exists in space "prod"/);
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars = { "A" = secret("prod") } } } }', /secret\(space, path, \[version\]\) takes 2 to 3 arguments/);
    check('  spec { workload { source { remote_image { image = "a" } } runtime { env_vars = { "A" = secrets("prod", "x") } } } }', /Unknown function secrets\(\)/, "secrets");
    check('  spec { workload { source { remote_image { image = "a" } } upgrade_strategy = "Rollover" } }', /Rollover strategy needs a readiness_signal/);
    check('  spec { workload { source { remote_image { image = "a" } } } networking { port_forwarding { protocol = "Tcp" host_port = 1 container_port = 1 ip_filter { prefix = prefix("300.1.1.1/8") } } } }', /not an IPv4 or IPv6 prefix/, '"300.1.1.1/8"');
    check('  spec { workload { source { remote_image { image = "a" } } } } scheduling { running = true }', /Only one scheduling block is allowed/);
});

test("missing fields fall back to zero values, except those that have none", () => {
    const text = `deployment {
  name = "min"
  space_id = space("prod")
  scheduling { placement { nodes = [node("primary")] } }
}`;
    const result = evaluateDeployment(text, catalog);
    assert.deepEqual(messages(result), ["Deployment requires spec."]);
    const withSpec = evaluateDeployment(text.replace('scheduling', 'spec { workload { source { remote_image { image = "a" } } } }\n  scheduling'), catalog);
    assert.deepEqual(messages(withSpec), []);
    assert.equal(withSpec.instance.spec.workload.runtime.default_volume.disabled, false);
    assert.equal(withSpec.instance.spec.networking.mode, "Virtual");
    assert.equal(withSpec.instance.spec.workload.readiness_signal, undefined);
    assert.equal(evaluateDeployment("deployment { name = \"n\" scheduling { placement { nodes = [node(\"primary\")] } } }", catalog).diagnostics[0].message, "Deployment requires space_id.");
});

test("running deployments need a version, and nix builds need a commit sha", () => {
    const base = version => `deployment {
  name = "nix"
  space_id = space("prod")
  spec { workload { source { nix_image_build { repo = "https://github.com/acme/api" flake = "." target = ".#image" ${version} } } } }
  scheduling { running = true placement { nodes = [node("primary")] } }
}`;
    assert.deepEqual(messages(evaluateDeployment(base(""), catalog)), ["nix_image_build needs a version, the full 40-character commit sha, while running is true."]);
    const untagged = base("").replace(/nix_image_build \{[^}]*\}/, 'remote_image { image = "ghcr.io/acme/api" }');
    assert.deepEqual(messages(evaluateDeployment(untagged, catalog)), ['Container image must include a tag or digest (image = "repository:tag") while running is true.']);
    assert.deepEqual(messages(evaluateDeployment(base('version = "main"'), catalog)), ["A Nix image build version is the full 40-character commit sha."]);
    const sha = "0123456789abcdef0123456789abcdef01234567";
    const ok = evaluateDeployment(base(`version = "${sha}"`), catalog);
    assert.deepEqual(messages(ok), []);
    assert.deepEqual(ok.instance.spec.workload.source, {nix_image_build: {repo: "https://github.com/acme/api", flake: ".", target: ".#image"}});
    assert.equal(ok.instance.spec.workload.version, sha);
    const printed = printDeployment(ok.instance, catalog);
    assert.match(printed, new RegExp(`nix_image_build \\{\\n\\s+repo\\s+= "https://github.com/acme/api"\\n\\s+flake\\s+= "."\\n\\s+target\\s+= ".#image"\\n\\s+version = "${sha}"\\n`));
    assert.ok(!/\n      version/.test(printed));
    assert.deepEqual(evaluateDeployment(printed, catalog).instance, ok.instance);
});

test("the generic evaluator and printer work for any root with declared functions", () => {
    const {schema, diagnostics} = compileSchema(`
        type Shelf root {
            id: u64
            title: string
            owner: Person.id
            books: []type Book {
                isbn: string
                rating: ?f64
                format: enum Format { Paper, Digital }
                tags: map<string, bool>
            }
            lookup: map<i32, string>
            origin: type Address {
                line: string
            }
            size: [2]u8
        }
        type Person root {
            id: u64
            name: string
        }
    `);
    assert.deepEqual(diagnostics, []);
    const people = [{id: 9, name: "ann"}];
    const functions = defineFunctions(schema, {
        person: {
            signature: "(name: string) -> Person.id",
            impl: ([name], ctx) => people.find(person => person.name === name)?.id ?? ctx.report(`No person named ${name}.`, ctx.args[0]),
        },
    });
    const text = `shelf {
  title = "mine"
  owner = person("ann")

  books {
    isbn   = "1"
    rating = 4.5
    format = "Digital"
    tags   = { "new" = true }
  }

  books {
    isbn = "2"
  }

  lookup = { 1 = "one", 2 = "two" }

  origin {
    line = "home"
  }

  size = [3, 4]
}
`;
    const result = evaluate(schema, "Shelf", text, {functions});
    assert.deepEqual(messages(result), []);
    assert.deepEqual(result.instance, {
        title: "mine",
        owner: 9,
        books: [
            {isbn: "1", rating: 4.5, format: "Digital", tags: {new: true}},
            {isbn: "2", format: "Paper", tags: {}},
        ],
        lookup: {1: "one", 2: "two"},
        origin: {line: "home"},
        size: [3, 4],
    });
    const printers = {"Person.id": id => `person(${JSON.stringify(people.find(person => person.id === id).name)})`};
    const printed = print(schema, "Shelf", result.instance, {printers});
    assert.equal(printed, text);
    assert.deepEqual(evaluate(schema, "Shelf", printed, {functions}).instance, result.instance);
    assert.match(messages(evaluate(schema, "Shelf", 'shelf { title = "t" owner = 9 }', {functions}))[0], /Expected a Person reference such as person\(name\)/);
    assert.match(messages(evaluate(schema, "Shelf", 'shelf { title = "t" owner = person("bob") }', {functions}))[0], /No person named bob/);
    assert.match(messages(evaluate(schema, "Shelf", 'shelf { title = "t" owner = person("ann") size = [1] }', {functions}))[0], /exactly 2 items/);
    assert.match(messages(evaluate(schema, "Shelf", 'shelf { title = "t" owner = person("ann") lookup = { x = "a" } }', {functions}))[0], /Map keys here are i32 integers/);
    assert.throws(() => defineFunctions(schema, {bad: {signature: "(a: ?string, b: string) -> Person.id", impl: () => 1}}), /required parameter b follows an optional one/);
    assert.throws(() => defineFunctions(schema, {bad: {signature: "(a: string) -> Nothing", impl: () => 1}}), /unknown return type|Unknown type/);
});

test("validateInstance rejects instances that do not fit the type", () => {
    const schema = deploymentSchema();
    const broken = structuredClone(expectedInstance);
    broken.spec.workload.source = {remote_image: {image: "a"}, nix_image_build: {repo: "r", flake: "f", target: "t"}};
    const result = validateInstance(schema, "Deployment", broken);
    assert.equal(result.ok, false);
    assert.match(result.diagnostics[0].message, /exactly one of nix_image_build, remote_image/);
    const missing = structuredClone(expectedInstance);
    delete missing.scheduling.restart_generation;
    assert.match(validateInstance(schema, "Deployment", missing).diagnostics[0].message, /Scheduling without restart_generation/);
    const wrongEnum = structuredClone(expectedInstance);
    wrongEnum.spec.networking.mode = "Bridge";
    assert.match(validateInstance(schema, "Deployment", wrongEnum).diagnostics[0].message, /"Bridge", which is not one of Virtual, Host/);
    assert.equal(validateInstance(schema, "Deployment", expectedInstance).ok, true);
});

test("the type definition text has no tags, laws, or metadata left in it", () => {
    assert.ok(!/@\d/.test(deploymentTypeDefinition));
    assert.ok(!/\blaws\b/.test(deploymentTypeDefinition));
    assert.ok(!/\[(key|min|max|min_length|max_items|min_items|mutable)/.test(deploymentTypeDefinition));
});
