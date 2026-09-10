CREATE TABLE risk_reviews (
  id uuid PRIMARY KEY,
  owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject_type text NOT NULL CHECK (subject_type IN ('user','ip','token','moderation')),
  subject_id text NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','confirmed','dismissed')),
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id, subject_type, subject_id)
);
CREATE INDEX risk_reviews_owner_status_idx ON risk_reviews(owner_id,status,updated_at DESC);
