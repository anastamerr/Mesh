ALTER TABLE nodes ADD COLUMN direct_candidates jsonb NOT NULL DEFAULT '[]'::jsonb
  CHECK (jsonb_typeof(direct_candidates) = 'array');
