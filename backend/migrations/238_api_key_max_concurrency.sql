ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS max_concurrency integer NOT NULL DEFAULT 0 CHECK (max_concurrency >= 0);
