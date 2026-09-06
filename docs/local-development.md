# Local development on this Mac

Go and PostgreSQL are installed in `~/.local/mesh-tools` using the existing user-local micromamba package manager. New zsh terminals find the tools through `~/.zprofile` and `~/.zshrc`. In an already-open terminal:

```sh
export PATH="$HOME/.local/mesh-tools/bin:$PATH"
go version
psql --version
```

## PostgreSQL

The local cluster lives in `~/.local/share/mesh/postgres`. It listens on **127.0.0.1:5432**, not the LAN. TCP connections use password authentication; the private Unix socket uses operating-system peer authentication.

There are separate `mesh` and `mesh_test` databases with separate non-superuser owners. Generated connection strings and the development operator key are saved in `apps/control-plane/.env` (ignored by Git, file permissions 0600). Do not paste that file into issues or commit it.

PostgreSQL is started manually; it is not configured to launch automatically after a Mac reboot.

```sh
pg_ctl -D "$HOME/.local/share/mesh/postgres" status
pg_ctl -D "$HOME/.local/share/mesh/postgres" -l "$HOME/.local/share/mesh/postgres.log" -w start
pg_ctl -D "$HOME/.local/share/mesh/postgres" -m fast -w stop
```

Do not start the Compose database at the same time: it uses the same local port. The native installation is an alternative to Compose.

## Backend

From the project root:

```sh
npm run db:migrate
npm run dev
```

To run all backend tests, including the real PostgreSQL concurrency test, from `apps/control-plane`:

```sh
node --env-file=.env --import tsx --test test/*.test.ts
```

The integration test creates and drops its own schema in `mesh_test`. The ordinary root `npm test` command only runs that test when `MESH_TEST_DATABASE_URL` is already set in the process environment.

To include the compiled agent integration test, build it first and add `MESH_TEST_AGENT_BINARY=/Users/anoos/Dev/Mesh/agent/bin/mesh-agent` to the test command's environment. Tests clean up their isolated schemas and temporary agent identities.

## Native agent

From `agent`:

```sh
export CC=/usr/bin/clang
go vet ./...
go test -race ./...
go build -o bin/mesh-agent ./cmd/mesh-agent
go run ./cmd/mesh-agent info
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o bin/mesh-agent.exe ./cmd/mesh-agent
```

The conda Go compiler expects a conda C compiler by default; `CC` selects the already installed Apple compiler for host builds and race tests. Windows cross-compilation with `CGO_ENABLED=0` does not need it.

Cross-compilation proves the agent compiles for Windows. It does not validate DPAPI at runtime, Windows service hosting, or WSL lifecycle behavior. See the agent README for local enrollment and foreground operation.
