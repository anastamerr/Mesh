CREATE TABLE relay_tickets (
  token_hash text PRIMARY KEY CHECK (token_hash ~ '^[a-f0-9]{64}$'),
  node_id uuid NOT NULL REFERENCES nodes(id),
  role text NOT NULL CHECK (role IN ('device','consumer')),
  expires_at timestamptz NOT NULL
);
CREATE INDEX relay_tickets_expiry_idx ON relay_tickets(expires_at);
