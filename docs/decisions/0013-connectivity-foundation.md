# ADR 0013: Incremental direct connectivity with relay fallback

Status: implemented LAN-direct foundation; internet traversal evaluation pending.

## Decision

Mesh keeps storage semantics and device identity independent from network path. A paired storage client obtains fresh connection information from the controller, races up to four authenticated private-LAN TCP candidates, and starts the existing encrypted relay after a 250 ms direct head start. The first connection to complete TLS 1.3 authentication against the paired device public-key fingerprint wins. A reachable endpoint with the wrong identity cannot win the race.

`mesh-agent run --direct-lan --root <directory>` listens with the paired device certificate and publishes current private IPv4 candidates with each heartbeat. Candidate publication is opt-in. The controller accepts at most eight unique TCP candidates, rejects loopback/public addresses and privileged ports, and returns candidates only while the node heartbeat is fresh. An omitted candidate list clears previous observations.

Clients retain an explicit `--server` direct-address escape hatch and a `--relay-only` recovery override. Existing paired agents that publish no candidates continue to use the relay. Direct connectivity is transport substitution only: manifests, grants, offsets, journals, checksums, publication, destination protection, and catalogue behavior do not change.

## Path-selection behavior

- Race connection establishment only. Never migrate an active HTTP request or transfer stream.
- Authenticate each route before selecting it. Close successful losing connections.
- Bound candidate fan-out to four client attempts even if the controller returns eight candidates.
- Start relay immediately when no direct candidate exists; otherwise give direct routes 250 ms before relay startup.
- Emit a user-visible message only when the selected path changes.
- Return one generic no-route error without reflecting candidate addresses or remote error bodies.
- A transfer interrupted by path loss recovers through the existing durable upload/download checkpoints on the next request or command retry.

## Security and availability boundary

The controller is a discovery authority, not a TLS trust authority for file bytes. Pairing pins the device key and controller-issued storage grants remain mandatory inside both direct and relayed connections. Direct LAN access does not enable controller-independent authorization: the agent continues to validate grants with the controller and fails closed during controller outages.

Only RFC 1918 IPv4 endpoints are published in this slice. Public candidates, IPv6, DNS names, NAT mappings, STUN/ICE, hole punching, browser-native transport, and offline signed grants require separate decisions and tests.

## Compatibility and rollback

Migration `006_direct_candidates.sql` adds a bounded JSON candidate observation with an empty default. Old agents remain valid because the heartbeat field is optional. New clients accept connection responses with no candidates and retain relay behavior. Operators can disable direct publication by omitting `--direct-lan`, force the compatibility path with `--relay-only`, or use an explicit trusted storage URL with `--server`.

## Required evidence before automatic internet direct

1. Existing relay baseline across two physical networks, including large transfers and interrupted resume.
2. LAN direct versus relay measurements on the same fixtures and old Windows reference laptop.
3. Sleep/resume, network-change, stale-candidate, wrong-identity, revocation, controller outage, and UDP-blocked tests.
4. A browser-client decision: management-only browser plus native transfer helper, or browser-native file transfer.
5. A bounded evaluation of established traversal implementations for Windows behavior, CPU/RAM, licensing, maintenance, and relay interoperability.

This milestone does not implement a VPN, custom cryptography, custom NAT traversal, global relay orchestration, transparent TCP stream migration, or offline authorization.
