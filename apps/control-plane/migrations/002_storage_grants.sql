CREATE TABLE storage_grants (
  token_hash text PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id),
  access text NOT NULL CHECK (access IN ('read', 'write', 'list')),
  collection_id text,
  expires_at timestamptz NOT NULL,
  CHECK ((access = 'list' AND collection_id IS NULL) OR
         (access IN ('read', 'write') AND collection_id IS NOT NULL AND collection_id ~ '^[a-f0-9]{64}$'))
);
CREATE INDEX storage_grants_expiry ON storage_grants(expires_at);
