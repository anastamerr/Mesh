# ADR 0010: Windows service lifecycle

Status: implemented and cross-compiled; live reboot, logout, sleep, and account-policy validation remain pending.

## Decision

Mesh installs as an automatic delayed-start Windows service named `MeshPersonalCloud`. The service invokes the same validated `run` path as the foreground CLI, with explicit absolute identity and storage directories. Service stop and system shutdown cancel one shared context, allowing heartbeat, storage, compute reconciliation, application routes, relay connections, and executor proxy processes to drain through their existing lifecycle boundaries.

The installer configures three bounded restart attempts after 15 seconds, one minute, and five minutes. Runtime messages use the Windows Application event log. Uninstall first stops the service and then removes only the service registration and event source; enrolled identity, user files, the dedicated WSL distribution, containers, and application volumes are preserved.

## Account and secret handling

Windows identity state and the paired private key remain protected with current-user DPAPI. The installer therefore requires the service account to exactly match the current enrolled Windows user. It accepts that account's password only over stdin and passes it directly to the Windows Service Control Manager; Mesh does not write it to configuration, arguments, logs, or its state directory. Installation requires an elevated terminal, explicit state and storage paths, and a readable enrolled identity before it mutates service state.

This intentionally avoids machine-scope DPAPI and LocalSystem, either of which would weaken identity ownership or make an existing enrollment unreadable. Operators must treat Windows account password rotation and `Log on as a service` policy as service-operability events.

## Remaining evidence

CI compiles the Windows-only service integration and runs the shared runner lifecycle tests on Windows. Before production claims, a real target laptop must demonstrate automatic start across reboot and logout, service-account profile/DPAPI access, graceful stop, recovery after forced process termination, WSL startup, sleep/resume, password rotation, and uninstall preservation.
