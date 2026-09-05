# ADR 0001: Native Go agent and modular TypeScript control plane

Status: accepted for the initial skeleton.

Mesh installs on an existing Windows machine; it does not require replacing Windows. Native files work independently of WSL compute. Go is chosen for agent and executor integration simplicity and long-term suitability. Rust is not planned as a rewrite target.

Use one NestJS/Fastify application and PostgreSQL. Keep physical nodes separate from execution environments. Workload reconciliation, file transfers, and the remote data gateway will have separate contracts.

## Initial security scope

The first backend slice uses a single operator key from the environment, short-lived one-use enrollment tokens, and randomly generated node bearer credentials expiring after 30 days. Only SHA-256 hashes of high-entropy enrollment/node credentials are persisted. This is development bootstrap authentication, not finished user authentication or key-bound node identity.

All non-loopback use requires HTTPS at an appropriately configured edge. Default binding is loopback; no CORS is enabled. Do not put the operator key in a browser application. Account login, certificate-bound identity, credential rotation, edge rate limiting, and gateway authorization must precede a public deployment.

Enrollment token consumption and node insertion share a transaction. If the response is lost after commit, the token remains consumed; the operator must revoke the unknown enrollment and enroll again. Do not claim enrollment replay is idempotent.

Heartbeat sequence is monotonic per credential and must survive agent restarts. Receipt time comes from PostgreSQL, not a client timestamp. A stale heartbeat does not refresh presence. Revoked nodes cannot update inventory. Presence describes controller observations, never application availability.

## Deferred decisions

WSL service-account and unattended boot behavior require a hardware spike. Caddy/frp remain candidate edge components. The HTTP heartbeat endpoint is an incremental backend slice; the eventual authenticated control stream will reuse the same node identity and observation rules rather than replacing the resource model.
