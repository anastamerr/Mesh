# Public controller and relay deployment

This deployment runs the Mesh controller, relay, PostgreSQL, an HTTPS edge, Prometheus, black-box probes, and Alertmanager on one public Linux server. Both laptops make outbound connections to the public relay, so neither physical network needs port forwarding. Storage remains encrypted end to end between paired Mesh devices; the relay only forwards the opaque stream.

## Prerequisites

- A public Linux server with Docker Engine and Docker Compose v2.
- Two distinct DNS names, such as `api.mesh.example.com` and `relay.mesh.example.com`, with A records pointing to the server. Add AAAA records only if inbound IPv6 is actually configured.
- Inbound TCP 80 and 443 allowed in both the host firewall and provider security group. Allow outbound DNS, NTP, and HTTPS.
- The repository checked out on the server. Keep its `deploy/production/secrets` directory in an encrypted backup; losing it while retaining the database makes the existing deployment unusable.

The relay hostname must reach this server as ordinary TCP/TLS. If the domain uses Cloudflare DNS, leave both records **DNS only** unless a compatible layer-4 proxy product is deliberately configured. An ordinary HTTP CDN proxy is not the raw HTTP/1.1 CONNECT path used here.

## Bootstrap and deploy

From `deploy/production` on the server:

```sh
cp .env.example .env
$EDITOR .env
node --env-file=.env bootstrap.mjs
docker compose --env-file .env config --quiet
docker compose --env-file .env build --pull
docker compose --env-file .env up -d
docker compose --env-file .env ps -a
```

Bootstrap generates independent PostgreSQL, operator, and relay secrets with private permissions, plus exact-host routing and probe targets. It preserves a complete existing secret set and refuses a partial one. The one-shot `migrate` service applies checksum-protected migrations before the controller starts.

Traefik obtains and renews Let's Encrypt certificates, redirects port 80 to HTTPS, routes the controller by HTTP hostname, and routes the relay by TLS SNI on the same port 443. Plain HTTP exists only between Traefik and the relay/controller on the named Docker proxy network. PostgreSQL is on a separate internal network; monitoring is on another network; no Docker socket is mounted. Only 80/443 are public. Prometheus and Alertmanager bind to host loopback.

Verify cutover:

```sh
curl --fail --show-error https://api.mesh.example.com/health/ready
curl --fail --show-error https://api.mesh.example.com/v1/network
openssl s_client -connect relay.mesh.example.com:443 -servername relay.mesh.example.com -alpn http/1.1 </dev/null
docker compose --env-file .env logs --tail=100 controller relay traefik prometheus
```

Replace the example names with the values in `.env`. The first response must be `{"status":"ok"}`, the network response must advertise the public relay origin, and the TLS probe must show a valid certificate and `http/1.1` ALPN. A bare HTTP request to the relay is not a valid functional test because it requires authenticated CONNECT framing.

The operator key is `secrets/admin_key`. Copy it only to the trusted operator machine as a mode-0600 file; never put it in a command argument, shell history, Compose environment, or logs. The relay key is mounted only into the controller and relay.

## Rate limits and monitoring

The controller has two layers of per-source token-bucket limiting: Traefik at the edge and a bounded in-process limiter after the trusted-proxy address is resolved. Pairing creation has a stricter independent limit. Relay CONNECT attempts are limited globally and per node in the relay, while waiting connections, active streams, authorization concurrency, idle time, and maximum lifetime remain separately bounded. Transfer bytes are not rate-throttled.

Use an SSH tunnel to view monitoring without making it public:

```sh
ssh -L 9090:127.0.0.1:9090 -L 9093:127.0.0.1:9093 user@mesh-server
```

Then open `http://127.0.0.1:9090/targets` and `http://127.0.0.1:9090/alerts`; Alertmanager is at `http://127.0.0.1:9093`. Prometheus scrapes controller, relay, and Traefik metrics and probes both public TLS endpoints. Rules cover internal target failure, public endpoint failure, certificate expiry, controller 5xx bursts, relay authorization failure, rate-limit pressure, and relay capacity. The checked-in Alertmanager receiver retains and groups alerts in its UI; add an email, webhook, PagerDuty, or equivalent receiver to `alertmanager.yml` for off-host notification.

Operational checks:

```sh
docker compose --env-file .env exec prometheus promtool check config /etc/prometheus/prometheus.yml
docker compose --env-file .env exec prometheus promtool check rules /etc/prometheus/alerts.yml
docker compose --env-file .env exec alertmanager amtool check-config /etc/alertmanager/alertmanager.yml
```

Back up the PostgreSQL volume and `secrets` together. Test restoration. Keep the host patched, review JSON access logs, and upgrade pinned images deliberately after reading security advisories.

## Pair a physical Windows device

Build `mesh-agent.exe` from `agent` and copy it to the IdeaPad. On that laptop, while it is on physical network A:

```powershell
.\mesh-agent.exe pair --server https://api.mesh.example.com --name "IdeaPad Gaming" --state-dir C:\ProgramData\Mesh\identity
```

Keep that command running. On the trusted operator machine, compare the displayed fingerprint directly with the laptop, then list and approve the challenge while supplying the operator key on stdin:

```sh
agent/bin/mesh-agent pair list --server https://api.mesh.example.com --operator-stdin < /private/admin_key
agent/bin/mesh-agent pair approve --server https://api.mesh.example.com --code XXXX-XXXX --fingerprint SHA256_FINGERPRINT --operator-stdin < /private/admin_key
```

Start the paired agent on Windows with a dedicated storage root. It discovers the controller-published relay automatically:

```powershell
.\mesh-agent.exe run --state-dir C:\ProgramData\Mesh\identity --root D:\MeshStorage
```

The laptop must stay awake. Guided setup or `mesh-agent service install` can configure automatic Windows hosting under the enrolled user; Mesh does not change power settings. Add `--compute` for the verified WSL executor and `--direct-lan` for automatic LAN storage selection.

## Validate two physical networks

Leave the Windows device on network A. Put the operator/client machine on a genuinely separate network B—for example, disconnect Wi-Fi and use a cellular hotspot. Build the local client as `agent/bin/mesh-agent`, place a private copy of the production operator key on it, and run:

```sh
export MESH_PUBLIC_CONTROLLER=https://api.mesh.example.com
export MESH_PUBLIC_RELAY=https://relay.mesh.example.com
export MESH_PUBLIC_NODE='IdeaPad Gaming'
export MESH_PUBLIC_OPERATOR_KEY_FILE=/private/admin_key
export MESH_PUBLIC_DEVICE_NETWORK='home-wifi-Za7lan'
export MESH_PUBLIC_CLIENT_NETWORK='cellular-hotspot'
npm run test:public
```

The test refuses identical network labels, requires the node to be online, verifies public HTTPS and relay discovery, confirms metrics are private, uploads a 256 MiB file plus 100 small files and an empty directory, attempts a killed-and-resumed retrieval, then SHA-256 compares the entire retrieved tree. It prints a small JSON evidence report and removes the large temporary fixture. Set `MESH_PUBLIC_TEST_BYTES` to another byte count between 16 MiB and 4 GiB for a longer soak.

Because Mesh intentionally does not collect device public IP addresses, the script records but cannot independently prove the physical network labels. Confirm the two access networks out of band before treating the report as WAN evidence.
