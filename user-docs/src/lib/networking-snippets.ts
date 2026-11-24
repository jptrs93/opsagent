export const hostMode = `network {
  mode = "host"
}`;

export const serviceEnvironment = `container {
  env_vars = {
    "PAYMENTS_API_HOST" = address("prod", "payments-api")
  }
}`;

export const applicationDial = `const host = process.env.PAYMENTS_API_HOST;
const res = await fetch(\`http://[\${host}]:8080/charges\`);`;

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
