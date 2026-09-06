CREATE TABLE collections (
  node_id uuid NOT NULL REFERENCES nodes(id),
  id text NOT NULL CHECK (id ~ '^[a-f0-9]{64}$'),
  name text NOT NULL,
  file_count integer NOT NULL CHECK (file_count BETWEEN 0 AND 10000),
  total_bytes bigint NOT NULL CHECK (total_bytes BETWEEN 0 AND 9007199254740991),
  confirmed_at timestamptz,
  PRIMARY KEY (node_id, id)
);
