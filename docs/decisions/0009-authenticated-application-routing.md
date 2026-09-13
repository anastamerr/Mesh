# ADR 0009: Authenticated application routing

Status: implemented and unit/integration tested; live WAN and WSL container validation remain pending.

## Decision

Applications are private by default. Docker exposes the declared port only as container metadata and never binds it to a Windows or Linux host address. The Linux executor accepts a typed proxy request containing the application UUID, current revision, and declared port. Before dialing, it inspects the exact Mesh-owned container and requires matching ownership, kind, revision, port, running state, and a single valid container address.

The controller issues separate device and operator tickets only when the application's current revision is both desired and observed running on an active paired node. Each ticket is stored hashed, expires within ten minutes, and references the workload. Relay authorization rechecks current workload and node state, so a stop, restart revision, expiration, or node revocation invalidates unused tickets immediately.

## Transport and isolation

Relay routes use `app-{workload UUID}`. Storage retains its original route, and every waiting-device queue and authorization subject includes the route, preventing cross-connection between storage, applications, or two applications on the same node. The relay remains an opaque byte forwarder.

The node wraps each application stream in TLS using the key approved during pairing. The operator's `workload connect` command obtains a fresh ticket, connects through the workload route, verifies that paired public-key fingerprint, and forwards a loopback-only local TCP listener. The relay's HTTPS identity and the inner paired-device identity are independent checks.

## Revocation and limits

New relay authorization fails as soon as desired or observed state no longer matches the running revision. An already established stream ends when its proxy or container stops; the relay does not inspect or actively revoke opaque bytes midstream. The first version supports raw TCP and one declared port, not public publishing, DNS, HTTP host routing, UDP, multiple ports, or anonymous links.

The route path is covered by controller HTTP tests, repository integration tests, relay queue-isolation tests, strict control-client validation, and executor ownership tests. A separate-network WAN test and a real Docker application inside the provisioned WSL environment are still required operational evidence.
