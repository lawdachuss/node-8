-- Create tunnel_sessions table to track Cloudflare tunnel URLs
-- This allows you to always find the current tunnel URL even if it changes

CREATE TABLE IF NOT EXISTS tunnel_sessions (
    id BIGSERIAL PRIMARY KEY,
    run_id INTEGER,
    url TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ DEFAULT NOW(),
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Create index for faster lookups
CREATE INDEX IF NOT EXISTS idx_tunnel_sessions_active ON tunnel_sessions(is_active, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_tunnel_sessions_run_id ON tunnel_sessions(run_id);

-- Add comment
COMMENT ON TABLE tunnel_sessions IS 'Tracks Cloudflare tunnel URLs for accessing the recorder UI';
COMMENT ON COLUMN tunnel_sessions.run_id IS 'GitHub Actions run number (if applicable)';
COMMENT ON COLUMN tunnel_sessions.url IS 'The trycloudflare.com URL for accessing the UI';
COMMENT ON COLUMN tunnel_sessions.started_at IS 'When the tunnel was first established';
COMMENT ON COLUMN tunnel_sessions.last_seen_at IS 'Last time the tunnel was verified as active';
COMMENT ON COLUMN tunnel_sessions.is_active IS 'Whether this tunnel is currently active';

-- Enable Row Level Security
ALTER TABLE tunnel_sessions ENABLE ROW LEVEL SECURITY;

-- Create policy to allow all operations
CREATE POLICY "Allow all operations on tunnel_sessions" ON tunnel_sessions
    FOR ALL
    USING (true)
    WITH CHECK (true);

-- Function to mark old tunnels as inactive when a new one starts
CREATE OR REPLACE FUNCTION mark_old_tunnels_inactive()
RETURNS TRIGGER AS $$
BEGIN
    -- Mark all other tunnels as inactive
    UPDATE tunnel_sessions
    SET is_active = FALSE
    WHERE id != NEW.id AND is_active = TRUE;
    
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Trigger to automatically mark old tunnels as inactive
DROP TRIGGER IF EXISTS trigger_mark_old_tunnels_inactive ON tunnel_sessions;
CREATE TRIGGER trigger_mark_old_tunnels_inactive
    AFTER INSERT ON tunnel_sessions
    FOR EACH ROW
    EXECUTE FUNCTION mark_old_tunnels_inactive();

-- View to get the current active tunnel
CREATE OR REPLACE VIEW current_tunnel AS
SELECT *
FROM tunnel_sessions
WHERE is_active = TRUE
ORDER BY started_at DESC
LIMIT 1;

COMMENT ON VIEW current_tunnel IS 'Returns the currently active tunnel URL';
