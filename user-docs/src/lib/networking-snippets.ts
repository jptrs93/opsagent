export const hostMode = `network {
  mode = "host"
}`;

export const addressReferences = `address("prod", "payments-api", 2)       # inbound address of one instance
service_address("prod", "payments-api")  # the deployment as a whole; no ordinal`;

export const serviceEnvironment = `container {
  env_vars = {
    "PAYMENTS_API_HOST" = address("prod", "payments-api", 0)
  }
}`;

export const dnsNames = `# every established instance of a deployment: one AAAA record per instance
{name}.space-{spaceId}.internal

# one specific instance
{ordinal}.{name}.space-{spaceId}.internal`;

export const ingressListener = `ingress {
  https {
    hostname       = "api.example.com"
    container_port = 5001
    cert           = acme()
    listen {
      node    = node("edge-1")
      address = "203.0.113.10"
    }
  }
}`;
