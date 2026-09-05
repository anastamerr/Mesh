# Mesh

**Your hardware. One cloud.** Install Mesh on spare hardware to store files and run applications remotely.

Start with [the product idea and roadmap](PROJECT.md), [architecture decision](docs/decisions/0001-foundation.md), and [HTTP contract](contracts/README.md).

## Current status

Backend foundation only. Implemented: PostgreSQL migrations, operator-authorized enrollment token creation, atomic one-use node enrollment, hashed expiring node credentials, validated heartbeats, observed presence, listing, revocation, and persisted lifecycle audit events.

The Go commands only report build/host information. No Windows service, WSL provisioning, transfers, Docker execution, remote tunnel, accounts, or UI exists yet.

## Local setup

For the user-local Go/PostgreSQL installation on the development Mac, see [local development instructions](docs/local-development.md). That installation can be used instead of the Compose database below.

Prerequisites: Node.js 22+, npm, Docker Compose (or a PostgreSQL 17 database). Go 1.23+ is only needed for native skeleton commands.

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
npm run typecheck
npm test
npm run build
```

Tests use an in-memory repository for HTTP/security behavior; they do not validate PostgreSQL locking. Optional PostgreSQL integration tests are described in the test source and enabled with `MESH_TEST_DATABASE_URL` pointing to a dedicated test database. Never use a production database for tests.

CI provisions PostgreSQL, runs migrations twice to check rerun behavior, and runs both HTTP and database tests. A separate CI job checks Go formatting, vets the native skeleton, and builds Linux and Windows targets. These checks execute when the repository is pushed to GitHub; adding the workflow does not mean it has already run.

See [agent instructions](agent/README.md) for native commands. No frontend workspace has been created.
