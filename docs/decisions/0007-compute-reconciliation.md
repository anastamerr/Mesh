# ADR 0007: Typed one-container workload reconciliation

Status: implemented controller/agent/executor, verified job-output collections, and verified-bundle WSL provisioning; live container lifecycle validation remains pending.

## Boundary

The controller owns desired state for explicitly placed jobs and applications. A stable `docker-linux` execution-environment record remains separate from its physical node and reports architecture, runtime version, readiness and last contact. Workload creation requires that ready environment. The Windows agent polls only its own assignments with its node credential and invokes a separate `mesh-executor` inside the dedicated `Mesh` WSL distribution. The executor, not the controller or native storage server, owns Docker integration.

The remote contract is deliberately not a shell API. A workload contains a digest-pinned image, a bounded argument array, CPU and memory limits, an optional confirmed input collection, and—for an application—one container port. Host paths, environment variables, privileged flags, socket mounts, device mappings, and arbitrary Docker options are not accepted.

## Desired and observed state

Workloads have stable caller-supplied UUIDs and monotonically increasing revisions. Repeating the same create request is idempotent; reusing an ID for another specification conflicts. Applications may be `running` or `stopped`. Jobs are run-to-completion and remain desired as `running`; their observed result is `succeeded` or `failed`.

Agents may report only the current desired revision. Within a revision, observations cannot move backward, and a terminal observation can only be repeated exactly. A zero-exit job moves through the retryable `exporting` state until its output is durably published; only then may it report `succeeded` with the confirmed collection ID. Revoked or expired nodes cannot fetch assignments or report observations. Desired state and the last observation remain visible when a node is unreachable; neither is proof that an application is currently available.

## Runtime restrictions and persistence

Container names and volume names derive only from validated workload UUIDs. Images must use `@sha256:` digests. Containers run read-only, with all Linux capabilities dropped, `no-new-privileges`, a 256-process limit, explicit CPU and memory limits, and a bounded non-executable temporary filesystem. The memory-plus-swap limit equals the memory limit, so workloads cannot additionally consume host swap. Each newly created container uses the local logging driver with three 10 MB rotating files, independently of the host daemon's logging defaults. Jobs have no network. Applications use a private bridge and expose one declared port; no host port is published by this slice.

Every workload receives a stable Docker volume mounted at `/mesh/data`. Replacing or stopping a container does not delete that volume. An input collection, when selected, must already be confirmed on the assigned node and is mounted read-only at `/mesh/input`. The executor will not remove a name collision unless its Mesh ownership labels match the workload and carry a valid revision.

Stopping or replacing an application also stops containers in Docker restart backoff or a paused state. Neither state is treated as proof that the application has stopped; the runtime must acknowledge the stop operation first.

After a successful job exit, the executor copies `/mesh/data` into a revision-specific private scratch directory under the approved native storage root. The native store scans, hashes, durably imports, verifies, and atomically publishes those files using the same immutable collection format as an upload. The node confirms the collection before its ID is attached to the terminal observation. Reconciliation retries exports and imports idempotently, so a controller outage cannot duplicate the job or publish partial results. The output appears in the ordinary catalogue as `<workload name> output` and can be retrieved with the existing `get` command.

The executor reconciles rather than blindly creates. Existing running containers and completed jobs are observed without duplication. A newer revision stops and replaces only its owned container while retaining the volume. Process input/output and Docker command output are bounded, and runtime details are reduced to typed failure codes before they cross the control boundary.

The agent admits at most two concurrent reconciliation operations. Readiness calls have a 30-second deadline and executor invocations have a 15-minute deadline; these bound control operations, not the runtime of a successfully started job. Failed process invocations leave assignments available for retry and do not manufacture terminal job observations. Runtime-reported failure observations retain their existing semantics. Storage publication retains its locking and durability barriers. These worker limits do not reserve aggregate CPU/RAM across workloads or enforce disk quotas.

Controller workload admission takes the node row lock in a separate statement before counting capacity. This gives the subsequent INSERT a fresh READ COMMITTED snapshot after a competing admission commits. The 100-assignment ceiling is therefore preserved under simultaneous creates. Completed jobs do not occupy assignment slots; applications continue to occupy a slot when stopped.

## Current limits

- `mesh-agent compute setup` imports the verified Docker/executor bundle into the dedicated `Mesh` WSL2 distribution. Enabling WSL Windows features can still require one elevated command and a reboot. Same-user Windows service hosting is implemented; in-place compute-bundle upgrades remain unsupported.
- Export scratch files are retained under the private storage metadata directory for idempotent recovery; automatic reclamation needs a bounded retention policy before long-running deployments.
- Application ports are routed only through the authenticated, paired-device channel in [ADR 0009](0009-authenticated-application-routing.md); they are never published on the host.
- There is no scheduling, multi-container definition, GPU access, arbitrary environment injection, or automatic relocation.
- Container runtime behavior is unit tested through a fake runtime and compile checked for Linux. A live Docker/WSL lifecycle test is still required before calling the compute experience production-ready.
