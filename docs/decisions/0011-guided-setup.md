# ADR 0011: Resumable guided host setup

Status: implemented and process-tested; signed release distribution remains pending.

## Decision

`mesh-agent setup` is the preferred backend-only installation entry point. One command validates the controller and device name, creates or resumes paired identity, initializes a dedicated native storage root, optionally verifies and imports the WSL compute bundle, and optionally installs automatic Windows service hosting.

The setup flow composes the same domain commands used independently rather than creating a second enrollment, provisioning, or service implementation. This keeps protocol validation, rollback rules, DPAPI handling, and diagnostics consistent. It prints the pairing code and public-key fingerprint for explicit operator approval; it does not weaken pairing into possession of an unauthenticated setup link.

## Safety and recovery

Identity state and contributed storage must be distinct absolute directory trees. Setup never overwrites an existing identity. A matching enrolled identity is reused on rerun; another controller or name fails closed. Pending pairing already persists its key and proof before the network request, while WSL import verifies the bundle before mutation and rolls back only a distribution known to have been created by that invocation.

Storage initialization occurs only after pairing succeeds. Compute is opt-in, and an existing named distribution must already pass Mesh doctor checks. Service installation remains opt-in because it requires elevation and the current Windows account password over stdin. A failure in a later stage retains earlier safe stages so the same command can continue after the cause is corrected.

## Remaining packaging work

The command removes the need for SSH, manual relay addressing, and a sequence of expert CLI operations. CI packages it with the Windows agent, compute bundle, checksums, and a PowerShell wrapper that verifies the agent before execution and offers an interactive service credential prompt. A future signed release installer can add publisher verification, release-manifest download, directory selection with explicit ACLs, and graphical progress without changing backend semantics.
