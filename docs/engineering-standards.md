# Mesh engineering standards

## Keep the design small

- Organize code around responsibilities: transport, local state, inventory, execution, and control-plane domains.
- Keep CLI parsing separate from state changes and network calls. Reject invalid commands before side effects.
- Extract shared behavior when it already has multiple callers, such as database transactions. Avoid speculative plugin systems, generic repositories, and wrappers that only rename an API.
- Keep production paths explicit and typed. Test doubles must match documented behavior, including ordering, bounds, and errors.
- Do not introduce UI, scheduling, or distributed-storage machinery into a maintenance pass.

## Preserve correctness at boundaries

- Validate external input; distinguish missing values from meaningful zero values.
- Validate success acknowledgements as well as status codes. Bound responses and local file reads.
- Never log credentials, environment dumps, database connection details, or untrusted response bodies.
- Make retries explicit. Persist state before a request when a lost acknowledgement could cause replay.
- Use database constraints/atomic predicates for concurrency. Test them against PostgreSQL, not only a mock.
- Repeated idempotent operations must not create duplicate effects or misleading audit events.
- Own resource cleanup clearly. Preserve the original error if cleanup also fails, and discard broken database clients.

## Performance claims need evidence

- Count expensive operations before adding caches or changing algorithms.
- Keep streaming and bounded memory as defaults for future file transfers.
- Preserve keep-alive connections where safe; avoid repeated setup on ordinary retries.
- Do not remove synchronization or durability to improve benchmark numbers.
- State the measured change narrowly. For example: the accepted heartbeat path uses one SQL statement instead of BEGIN/SELECT/UPDATE/COMMIT. This is not a claim of fourfold end-to-end throughput.

## Verification before a commit

- TypeScript/JavaScript: `npm run lint` runs every generic anti-slop rule at error severity across source, tests and scripts. Vendored rule source and generated/agent assets are excluded. Do not disable rules or disguise types to satisfy them; parse external input at the HTTP/JSON boundary, preserve inferred types, and use real typed dependency seams in tests. Any necessary type assertion must explain its checked invariant with a `SAFETY:` comment.
- TypeScript: strict typecheck (including unused declarations), build, HTTP tests, PostgreSQL integration tests, and the compiled-agent integration test.
- Go: gofmt, go vet, race tests, host build, and Windows cross-build. CI additionally runs native tests on Linux, macOS, and Windows.
- Add regression tests for concrete bugs and failure modes. Do not inflate coverage with assertions that merely mirror implementation.
- Review coverage gaps, but distinguish unit instrumentation from behavior exercised through compiled integration tests. Do not describe cross-compilation as Windows runtime validation.
- Keep credentials and generated binaries out of Git. Update contracts and architecture notes when guarantees change.
- Report any checks not run and limitations still present. No claim that a small test suite proves production readiness or optimal performance.

## Anti-slop tooling

Rules are vendored from [dmmulroy/anti-slop](https://github.com/dmmulroy/anti-slop) under `tools/oxlint/anti-slop`; source revision and license are recorded alongside them. The plugin is development tooling for TypeScript/JavaScript, not a Go analyzer. Keep Oxlint and its plugin runtime pinned together. Effect-specific rules are not enabled because Mesh does not use Effect. CI runs lint before the backend checks.
