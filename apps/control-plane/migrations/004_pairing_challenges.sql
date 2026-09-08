ALTER TABLE nodes ADD COLUMN public_key_fingerprint text UNIQUE
  CHECK (public_key_fingerprint ~ '^[a-f0-9]{64}$');

CREATE TABLE pairing_challenges (
  id uuid PRIMARY KEY,
  request_id uuid UNIQUE NOT NULL,
  code text UNIQUE NOT NULL CHECK (code ~ '^[A-Z2-9]{4}-[A-Z2-9]{4}$'),
  pairing_secret_hash text NOT NULL CHECK (pairing_secret_hash ~ '^[a-f0-9]{64}$'),
  node_credential_hash text NOT NULL CHECK (node_credential_hash ~ '^[a-f0-9]{64}$'),
  public_key_fingerprint text NOT NULL CHECK (public_key_fingerprint ~ '^[a-f0-9]{64}$'),
  name text NOT NULL,
  platform text NOT NULL CHECK (platform IN ('windows', 'linux', 'darwin')),
  architecture text NOT NULL CHECK (architecture IN ('amd64', 'arm64')),
  agent_version text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  approved_at timestamptz,
  node_id uuid UNIQUE REFERENCES nodes(id),
  CHECK ((approved_at IS NULL AND node_id IS NULL) OR (approved_at IS NOT NULL AND node_id IS NOT NULL))
);

CREATE INDEX pairing_challenges_pending_idx ON pairing_challenges (created_at DESC)
  WHERE approved_at IS NULL;
