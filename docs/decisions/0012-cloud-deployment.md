# ADR 0012: Self-hosted cloud deployment

Status: implemented and Compose-validated; public-host and WAN validation remain pending.

## Decision

The supported initial cloud layout is a single-host Compose stack containing PostgreSQL 17, the TypeScript control plane, the Go relay, and Caddy. Caddy terminates automatically managed HTTPS for the ordinary control API. The relay retains its native HTTP/1.1 CONNECT-aware TLS listener on port 7443 instead of depending on generic reverse-proxy tunnel behavior.

PostgreSQL and the control plane are reachable only on a private Compose network. Public ports are limited to 80/443 for controller certificate management and HTTPS, plus 7443 for relay TLS. The controller runs checksum-verified migrations under an advisory lock before it starts listening, and dependent services wait for readiness checks.

## Secrets and persistence

Database URLs, operator credentials, and relay credentials are mounted as Compose secret files. The controller accepts bounded regular secret files, rejects symlinks and ambiguous environment overrides, and never includes values in configuration errors. The relay reads the same independent relay credential from its own file. Operator and relay credentials must differ.

PostgreSQL data and Caddy certificate state use named volumes. Relay certificates are explicit secret mounts because the native relay terminates its own TLS; renewal and container recreation remain an administrator operation. Database backups are separate from volume persistence and are required before calling a deployment protected.

## Scope

This deployment is for one owner and explicitly placed personal devices. It is not a hosted multi-tenant service. Public-host smoke tests, firewall behavior, certificate renewal, restore from database backup, and separate-network storage/application traffic remain release evidence rather than assumptions.
