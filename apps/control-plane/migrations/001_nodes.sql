CREATE TABLE enrollment_tokens (
  token_hash text PRIMARY KEY,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE nodes (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  platform text NOT NULL CHECK (platform IN ('windows', 'linux', 'darwin')),
  architecture text NOT NULL CHECK (architecture IN ('amd64', 'arm64')),
  agent_version text NOT NULL,
  credential_hash text UNIQUE NOT NULL,
  credential_expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz,
  revoked_at timestamptz,
  heartbeat_sequence bigint NOT NULL DEFAULT -1,
  inventory jsonb
);

CREATE INDEX nodes_created_at_idx ON nodes (created_at DESC);

CREATE TABLE audit_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  action text NOT NULL,
  node_id uuid REFERENCES nodes(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
