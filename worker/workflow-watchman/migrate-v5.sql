-- migration: pool_autopilot
-- Tracks channels the autopilot worker has auto-added so manual removals are
-- remembered and never re-added. Run this once in the Supabase SQL editor.

CREATE TABLE IF NOT EXISTS pool_autopilot (
  username   TEXT PRIMARY KEY,
  gender     TEXT NOT NULL,
  viewers    INT NOT NULL DEFAULT 0,
  added_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pool_autopilot_gender ON pool_autopilot(gender);

ALTER TABLE pool_autopilot ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS "Allow all operations on pool_autopilot" ON pool_autopilot;
CREATE POLICY "Allow all operations on pool_autopilot" ON pool_autopilot
  FOR ALL USING (true) WITH CHECK (true);
