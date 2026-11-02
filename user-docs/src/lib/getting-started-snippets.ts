const installer = 'curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/install_primary.sh | bash -s --';

export const installHttp = `${installer} \\
  --http-only true
`;

export const installManaged = `${installer} \\
  --web-hosts opendeploy.example.com
`;

export const installManagedPrivate = `# Obtain the certificate with a DNS challenge, then hand the bundle to the installer.
sudo certbot certonly --manual --preferred-challenges dns -d opendeploy.example.com
sudo cat /etc/letsencrypt/live/opendeploy.example.com/fullchain.pem /etc/letsencrypt/live/opendeploy.example.com/privkey.pem > /root/opendeploy.pem

${installer} \\
  --web-tls-cert-pem-file /root/opendeploy.pem \\
  --web-hosts opendeploy.example.com
`;

export const installSelfManaged = `${installer} \\
  --web-tls-cert-pem-file /root/opendeploy.pem \\
  --web-hosts opendeploy.example.com
`;

export const installSelfSigned = `${installer} \\
  --web-tls-self-signed true \\
  --web-hosts opendeploy.internal
`;

export const installOutput = `Master password: opendeploy-...
Use this password under "Enrol or recover" on the web UI sign-in page to create the first administrator.

Install complete. opendeploy.service is enabled and started.

Web UI:
  https://opendeploy.example.com
`;

export const firstDeployment = `deployment {
  name  = "hello"
  space = space("global")

  container {
    source {
      remote_image {
        image = "docker.io/library/nginx:1.27"
      }
    }
  }

  network {
    ingress {
      port_forward {
        protocol       = "tcp"
        container_port = 80
        host_port      = 8081
      }
    }
  }

  scheduling {
    running = true
    dedicated_nodes {
      nodes = [node("primary")]
    }
  }
}
`;

export const checkIt = `curl http://your-machine:8081/
`;

export const changeIt = `image = "docker.io/library/nginx:1.28"
`;

export const uninstall = `# Stop and remove OpenDeploy, keeping its data for a later reinstall.
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/uninstall.sh | bash -s --

# Remove everything, including data.
curl -fsSL https://raw.githubusercontent.com/jptrs93/opsagent/main/scripts/uninstall.sh | bash -s -- --purge
`;
