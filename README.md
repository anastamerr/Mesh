# Mesh

**Your hardware. One cloud.** Install Mesh on spare hardware to store files and run applications remotely.

Start with [the product idea and roadmap](PROJECT.md), [architecture decision](docs/decisions/0001-foundation.md), and [HTTP contract](contracts/README.md).

Follow [the engineering standards](docs/engineering-standards.md) for modularity, performance claims, and pre-commit verification.

## Current status

Backend and first working native-agent slice. Implemented: PostgreSQL migrations, operator-authorized enrollment token creation, atomic one-use node enrollment, hashed expiring node credentials, validated heartbeats, observed presence, listing, revocation, and persisted lifecycle audit events.

The Go agent enrolls, stores its identity, reports real CPU/RAM inventory, and sends heartbeats with durable sequences and reconnect backoff. The Linux executor remains a skeleton. Native storage now supports direct authenticated folder uploads, durable resumption, checksum verification, listing and retrieval. Enrolled storage validates short-lived, node/collection-scoped permissions with the controller on every request; shared storage keys are loopback-only development mode. See [the authorized storage workflow](docs/decisions/0004-storage-authorization.md). No Windows service, WSL provisioning, Docker execution, remote tunnel, accounts, or UI exists yet. See [agent commands and guarantees](agent/README.md).

## Local setup

For the user-local Go/PostgreSQL installation on the development Mac, see [local development instructions](docs/local-development.md). That installation can be used instead of the Compose database below.

Prerequisites: Node.js 22.18+, npm, Docker Compose (or a PostgreSQL 17 database). Go 1.25+ is needed for the native agent.

```sh
npm install
docker compose -f deploy/compose.yaml up -d
cp apps/control-plane/.env.example apps/control-plane/.env
```

Replace `MESH_ADMIN_KEY` in that `.env` with a random value. Generate one using:

```sh
node -e "console.log(require('node:crypto').randomBytes(32).toString('base64url'))"
```

Then:

```sh
npm run db:migrate
npm run dev
```

The API binds to `127.0.0.1:3000`. Visit `/health/ready` to verify the database schema. Root npm workspace scripts run with the control-plane directory as working directory, where `.env` is loaded. For compiled startup, run `npm run start --workspace @mesh/control-plane` after building.

The Compose password is local development only and PostgreSQL is bound to loopback. This slice is not ready for public exposure. Use HTTPS for credentials outside loopback; full account authentication, node key binding, rotation, and rate limiting remain planned work.

## Verify

```sh
npm run lint
npm run typecheck
npm test
npm run build
```

Tests use an in-memory repository for HTTP/security behavior; they do not validate PostgreSQL locking. Optional PostgreSQL integration tests are described in the test source and enabled with `MESH_TEST_DATABASE_URL` pointing to a dedicated test database. Never use a production database for tests.

Set `MESH_TEST_AGENT_BINARY` to the absolute path of a compiled agent to also run the real-agent integration test against an isolated PostgreSQL schema and temporary HTTP server. It verifies enrollment, sequence persistence across agent processes and controller restart, and revocation.

CI provisions PostgreSQL, runs migrations twice, builds the agent, and runs the complete integration suite. Native jobs check Go formatting, vet, and race tests on Linux, macOS, and Windows. These checks execute when the repository is pushed to GitHub; adding the workflow does not mean it has already run.

See [agent instructions](agent/README.md) for native commands. No frontend workspace has been created.
