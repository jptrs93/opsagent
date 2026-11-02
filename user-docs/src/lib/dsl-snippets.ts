export const minimalExample = `deployment {
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
          "REDIS_HOST"        = address("prod", "redis")
          "PORT"              = "8080"
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

export const minimalInstance = `{
  "name": "api",
  "space_id": 2,
  "spec": {
    "workload": {
      "source": { "remote_image": { "image": "ghcr.io/acme/api" } },
      "runtime": {
        "env_vars": {
          "DATABASE_PASSWORD": { "secret_env": { "secret": { "secret_id": 11, "version": 3 } } },
          "REDIS_HOST": { "address_env": { "deployment_id": 7, "space_id": 2 } },
          "PORT": { "literal_env": { "value": "8080" } }
        }
      },
      "version": "1.4.2"
    }
  },
  "scheduling": {
    "running": true,
    "placement": { "nodes": [4] }
  }
}
`;


export const statementsExample = `name = "api"            # attribute: a scalar field

scheduling {            # block: a field whose type is a structure
  running = true
}

asset_mounts {          # repeated block: a list of structures, one block per item
  asset          = asset("prod", "google/sa.json")
  container_path = "/etc/sa.json"
}

asset_mounts {
  asset          = asset("prod", "ca.pem")
  container_path = "/etc/ca.pem"
}
`;

export const choiceExample = `# Block form: the inner block names the alternative.
source {
  remote_image {
    image = "ghcr.io/acme/api"
  }
}

# Value form: the type of the value picks the alternative.
env_vars = {
  "PORT"     = "8080"                          # a string is a literal value
  "PASSWORD" = secret("prod", "db/password")   # a secret reference is a secret value
}
cert_source = acme()                           # a value returned by a function
`;

export const defaultsExample = `networking {
  port_forwarding {
    host_port      = 5432      # protocol is left out, so it is "Tcp"
    container_port = 5432
  }
}
`;

export const diagnosticsExample = `15: Secret references may only use the deployment's space or the global space.   "staging"
22: Expected a quoted string.                                                     7
38: "rolling" is not one of "Recreate", "Rollover".                               "rolling"
77: Expected a Node reference such as node(name).                                 "worker-1"
`;

export const codeGrammar = `document   = statement*
statement  = name "=" expression
           | name "{" statement* "}"
expression = string | number | "true" | "false"
           | name "(" [expression ("," expression)*] ")"
           | "[" [expression ("," expression)*] "]"
           | "{" (key "=" expression)* "}"
key        = name | string | integer
`;

export const typeExcerpt = `type SecretRef {
    secret_id: Secret.id
    version: u32
}

type Scheduling {
    running: bool
    placement: type DedicatedNodesScheduling {
        nodes: []Node.id
    }
    restart_generation: u32
}

enum NetworkingMode {
    Virtual
    Host
}
`;

export const grammar = `definition   = declaration*
declaration  = "type" Name ["root" | "mat"] "{" field* "}"
             | "enum" Name "{" (Case [";" | ","])* "}"
             | "id" Name "=" primitive [";"]
field        = name ":" type_expr [";" | ","]
type_expr    = unary ("|" unary)*
unary        = "?" unary
             | "[" "]" unary
             | "[" integer "]" unary
             | "(" type_expr ")"
             | "map" "<" type_expr "," type_expr ">"
             | "type" Name "{" field* "}"
             | "enum" Name "{" (Case [";" | ","])* "}"
             | primitive
             | Name ("." Name)*
primitive    = "bool" | "string" | "bytes" | "uuid"
             | "i32" | "i64" | "u8" | "u32" | "u64" | "f32" | "f64"
`;
