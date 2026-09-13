CREATE TABLE execution_environments (
  id uuid PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id),
  kind text NOT NULL CHECK (kind = 'docker-linux'),
  architecture text NOT NULL CHECK (architecture IN ('amd64', 'arm64')),
  status text NOT NULL CHECK (status IN ('ready', 'unavailable')),
  runtime_version text CHECK (runtime_version ~ '^[A-Za-z0-9.+-]{1,40}$'),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (node_id, kind),
  CHECK ((status = 'ready') = (runtime_version IS NOT NULL))
);

CREATE TABLE workloads (
  id uuid PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id),
  execution_environment_id uuid NOT NULL REFERENCES execution_environments(id),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
  kind text NOT NULL CHECK (kind IN ('job', 'application')),
  image text NOT NULL CHECK (image ~ '@sha256:[a-f0-9]{64}$'),
  command jsonb NOT NULL CHECK (jsonb_typeof(command) = 'array' AND jsonb_array_length(command) BETWEEN 1 AND 64),
  cpu_millis integer NOT NULL CHECK (cpu_millis BETWEEN 100 AND 64000),
  memory_bytes bigint NOT NULL CHECK (memory_bytes BETWEEN 67108864 AND 9007199254740991),
  input_collection_id text CHECK (input_collection_id ~ '^[a-f0-9]{64}$'),
  service_port integer CHECK (service_port BETWEEN 1 AND 65535),
  desired_state text NOT NULL CHECK (desired_state IN ('running', 'stopped')),
  creation_hash text NOT NULL CHECK (creation_hash ~ '^[a-f0-9]{64}$'),
  revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
  observed_revision bigint CHECK (observed_revision BETWEEN 1 AND revision),
  observed_state text NOT NULL DEFAULT 'pending'
    CHECK (observed_state IN ('pending', 'pulling', 'starting', 'running', 'exporting', 'succeeded', 'failed', 'stopped')),
  exit_code integer CHECK (exit_code BETWEEN 0 AND 255),
  failure_code text CHECK (failure_code IN ('image-unavailable', 'invalid-runtime', 'resource-unavailable', 'runtime-failure')),
  output_collection_id text CHECK (output_collection_id ~ '^[a-f0-9]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  observed_at timestamptz,
  CHECK ((kind = 'application' AND service_port IS NOT NULL) OR (kind = 'job' AND service_port IS NULL)),
  CHECK (kind = 'application' OR desired_state = 'running'),
  CHECK ((observed_state = 'failed') = (failure_code IS NOT NULL)),
  CHECK ((observed_state IN ('succeeded', 'failed')) = (exit_code IS NOT NULL)),
  CHECK ((observed_state = 'succeeded') = (output_collection_id IS NOT NULL)),
  CHECK ((observed_state = 'pending') = (observed_revision IS NULL AND observed_at IS NULL)),
  FOREIGN KEY (node_id, input_collection_id) REFERENCES collections(node_id, id),
  FOREIGN KEY (node_id, output_collection_id) REFERENCES collections(node_id, id)
);

CREATE INDEX workloads_node_idx ON workloads(node_id, created_at, id);
CREATE INDEX workloads_updated_idx ON workloads(updated_at DESC, id DESC);
