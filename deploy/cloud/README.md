# Self-hosted Mesh cloud

This stack runs the single-owner control plane, PostgreSQL, public TLS edge, and opaque relay. It publishes only controller HTTPS on ports 80/443 and the native TLS relay on 7443; PostgreSQL and the controller's cleartext application port stay on the private Compose network.

## Prepare

1. Point `MESH_CONTROL_HOST` and `MESH_RELAY_HOST` DNS records at this server.
2. Run `sh initialize.sh`, then edit the generated `.env` with both hostnames. The initializer uses a private umask and never overwrites an existing credential.
3. Review every file described in [`secrets/README.md`](secrets/README.md). The generated database URL and password already match.
4. Obtain a certificate and private key for the relay hostname and save them as `secrets/relay_tls_cert.pem` and `secrets/relay_tls_key.pem`. Caddy obtains and renews the separate controller certificate.
5. Restrict the deployment directory and secret files to the deployment administrator.

Then validate and start:

```sh
docker compose --env-file .env config --quiet
docker compose --env-file .env up --build -d
docker compose --env-file .env ps
curl --fail https://control.mesh.example/health/ready
```

The control-plane container applies checksum-pinned migrations under a PostgreSQL advisory lock before serving. Services wait for PostgreSQL and controller readiness, restart after host reboot, and retain database and Caddy state in named volumes.

Use `https://<MESH_CONTROL_HOST>` as the host setup controller. The configured relay origin is `https://<MESH_RELAY_HOST>:7443/`. Both public endpoints need inbound TCP firewall rules; old laptops need only outbound HTTPS/TCP and no router forwarding.

## Operations

Back up the PostgreSQL volume independently before upgrades. Review migration changes before replacing images. Rotate the operator and relay credentials by replacing their files and recreating the affected containers; the two values must remain different. Relay certificate renewal currently belongs to the deployment administrator and requires recreating the relay container after the files change.

Do not publish port 3000 or 5432. This is the intentionally single-owner deployment: it does not provide multi-user accounts, anonymous shares, or public application links.
