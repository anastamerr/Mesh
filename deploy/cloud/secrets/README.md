# Cloud secret files

Create these private files before starting the cloud stack. Each contains one value and a trailing newline is allowed.

- `postgres_password`: random PostgreSQL password.
- `database_url`: `postgresql://mesh:<URL-encoded password>@postgres:5432/mesh`.
- `admin_key`: independent 32–256 character URL-safe operator credential.
- `relay_key`: different 32–256 character URL-safe relay credential.
- `relay_tls_cert.pem`: public certificate chain for the relay hostname.
- `relay_tls_key.pem`: matching private key.

Restrict this directory to the deployment administrator. It is ignored by Git. The relay certificate must cover `MESH_RELAY_HOST`; Caddy obtains the separate controller certificate automatically.
