-- ============================================================================
-- Chaturbate DVR - Complete Supabase Database Schema
-- ============================================================================
-- Run this ONCE in your Supabase SQL Editor to set up the database.
-- Safe to re-run: uses IF NOT EXISTS / ADD COLUMN IF NOT EXISTS everywhere.
--
-- Multi-instance support: each fork sets INSTANCE_ID env var (a, b, c...).
-- Channels are namespaced in app_settings as "channels_<instance_id>".
-- Tunnels and tunnel_sessions are filtered by instance_id.
-- Recordings, uploads, previews, logs are shared across all instances.
-- ============================================================================

-- Ensure public schema exists and is in the search path
CREATE SCHEMA IF NOT EXISTS public;
SET search_path TO public;

-- gen_random_uuid() is built-in since PostgreSQL 13; no extension needed.

-- ============================================================================
-- 1. CHANNELS
-- ============================================================================
CREATE TABLE IF NOT EXISTS channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(255) UNIQUE NOT NULL,
    is_paused BOOLEAN DEFAULT FALSE,
    framerate INTEGER DEFAULT 30,
    resolution INTEGER DEFAULT 1080,
    pattern TEXT DEFAULT 'videos/{{.Username}}_{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}-{{.Minute}}-{{.Second}}{{if .Sequence}}_{{.Sequence}}{{end}}',
    max_duration INTEGER DEFAULT 0,
    max_filesize INTEGER DEFAULT 0,
    compress BOOLEAN DEFAULT FALSE,
    created_at BIGINT NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_channels_username ON channels(username);
CREATE INDEX IF NOT EXISTS idx_channels_created_at ON channels(created_at);

-- ============================================================================
-- 2. RECORDINGS
-- ============================================================================
CREATE TABLE IF NOT EXISTS recordings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID REFERENCES channels(id) ON DELETE CASCADE,
    username VARCHAR(255) NOT NULL,
    filename VARCHAR(500) UNIQUE NOT NULL,
    timestamp TIMESTAMP WITH TIME ZONE NOT NULL,
    room_title TEXT,
    tags TEXT[],
    viewers INTEGER DEFAULT 0,
    resolution VARCHAR(50),
    framerate INTEGER,
    filesize BIGINT DEFAULT 0,
    gender VARCHAR(50),
    thumbnail_url TEXT,
    sprite_url TEXT,
    embed_url TEXT,
    preview_url TEXT,
    instance_id TEXT NOT NULL DEFAULT 'default',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_recordings_username ON recordings(username);
CREATE INDEX IF NOT EXISTS idx_recordings_filename ON recordings(filename);
CREATE INDEX IF NOT EXISTS idx_recordings_timestamp ON recordings(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_recordings_channel_id ON recordings(channel_id);
CREATE INDEX IF NOT EXISTS idx_recordings_gender ON recordings(gender);
CREATE INDEX IF NOT EXISTS idx_recordings_instance ON recordings(instance_id);

-- ============================================================================
-- 3. UPLOAD_LINKS
-- ============================================================================
CREATE TABLE IF NOT EXISTS upload_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    recording_id UUID REFERENCES recordings(id) ON DELETE CASCADE,
    host VARCHAR(100) NOT NULL,
    url TEXT NOT NULL,
    instance_id TEXT NOT NULL DEFAULT 'default',
    uploaded_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_upload_links_recording_id ON upload_links(recording_id);
CREATE INDEX IF NOT EXISTS idx_upload_links_host ON upload_links(host);
CREATE INDEX IF NOT EXISTS idx_upload_links_instance ON upload_links(instance_id);

-- ============================================================================
-- 4. APP_SETTINGS
-- ============================================================================
CREATE TABLE IF NOT EXISTS app_settings (
    key VARCHAR(255) PRIMARY KEY,
    value JSONB NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- ============================================================================
-- 5. TUNNELS
-- ============================================================================
CREATE TABLE IF NOT EXISTS tunnels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    url TEXT NOT NULL,
    run_id INTEGER,
    is_active BOOLEAN DEFAULT TRUE,
    instance_id TEXT NOT NULL DEFAULT 'default',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    expires_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_tunnels_active ON tunnels(is_active, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tunnels_run_id ON tunnels(run_id);
CREATE INDEX IF NOT EXISTS idx_tunnels_instance ON tunnels(instance_id);

-- ============================================================================
-- 6. TUNNEL_SESSIONS
-- ============================================================================
CREATE TABLE IF NOT EXISTS tunnel_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id INTEGER NOT NULL,
    url TEXT NOT NULL,
    instance_id TEXT NOT NULL DEFAULT 'default',
    started_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    last_seen_at TIMESTAMP WITH TIME ZONE,
    is_active BOOLEAN DEFAULT TRUE
);

CREATE INDEX IF NOT EXISTS idx_tunnel_sessions_run ON tunnel_sessions(run_id);
CREATE INDEX IF NOT EXISTS idx_tunnel_sessions_instance ON tunnel_sessions(instance_id);
CREATE INDEX IF NOT EXISTS idx_tunnel_sessions_active ON tunnel_sessions(is_active, started_at DESC);

-- ============================================================================
-- 7. CHANNEL_LOGS
-- ============================================================================
CREATE TABLE IF NOT EXISTS channel_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID REFERENCES channels(id) ON DELETE CASCADE,
    username VARCHAR(255) NOT NULL,
    log_level VARCHAR(20) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_channel_logs_channel_id ON channel_logs(channel_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_channel_logs_username ON channel_logs(username, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_channel_logs_created_at ON channel_logs(created_at DESC);

-- ============================================================================
-- 8. RECORDING_SESSIONS
-- ============================================================================
CREATE TABLE IF NOT EXISTS recording_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID REFERENCES channels(id) ON DELETE CASCADE,
    username VARCHAR(255) NOT NULL,
    started_at TIMESTAMP WITH TIME ZONE NOT NULL,
    ended_at TIMESTAMP WITH TIME ZONE,
    duration_seconds INTEGER,
    room_status VARCHAR(50),
    is_online BOOLEAN DEFAULT FALSE,
    sequence INTEGER DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_recording_sessions_channel_id ON recording_sessions(channel_id);
CREATE INDEX IF NOT EXISTS idx_recording_sessions_username ON recording_sessions(username);
CREATE INDEX IF NOT EXISTS idx_recording_sessions_started_at ON recording_sessions(started_at DESC);

-- ============================================================================
-- 9. PREVIEW_IMAGES
-- ============================================================================
CREATE TABLE IF NOT EXISTS preview_images (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    recording_id UUID REFERENCES recordings(id) ON DELETE CASCADE,
    filename VARCHAR(500) NOT NULL,
    thumbnail_url TEXT,
    sprite_url TEXT,
    github_path TEXT,
    preview_url TEXT,
    instance_id TEXT NOT NULL DEFAULT 'default',
    uploaded_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(filename)
);

CREATE INDEX IF NOT EXISTS idx_preview_images_recording_id ON preview_images(recording_id);
CREATE INDEX IF NOT EXISTS idx_preview_images_filename ON preview_images(filename);
CREATE INDEX IF NOT EXISTS idx_preview_images_instance ON preview_images(instance_id);

-- ============================================================================
-- 10. UPLOAD_JOURNAL
-- ============================================================================
-- Tracks per-host upload state for crash recovery. On restart the app queries
-- this table to determine which hosts already received a given file.
CREATE TABLE IF NOT EXISTS upload_journal (
    id UUID DEFAULT gen_random_uuid() PRIMARY KEY,
    file_hash TEXT NOT NULL,
    filename TEXT NOT NULL,
    host TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    error_msg TEXT,
    file_size BIGINT,
    instance_id TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(file_hash, host)
);

CREATE INDEX IF NOT EXISTS idx_upload_journal_hash ON upload_journal(file_hash);
CREATE INDEX IF NOT EXISTS idx_upload_journal_status ON upload_journal(status);

-- ============================================================================
-- 11. DISK_USAGE
-- ============================================================================
CREATE TABLE IF NOT EXISTS disk_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    total_bytes BIGINT NOT NULL,
    used_bytes BIGINT NOT NULL,
    free_bytes BIGINT NOT NULL,
    percent_used INTEGER NOT NULL,
    recorded_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_disk_usage_recorded_at ON disk_usage(recorded_at DESC);

-- ============================================================================
-- FUNCTIONS AND TRIGGERS
-- ============================================================================

CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_channels_updated_at BEFORE UPDATE ON channels
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_recordings_updated_at BEFORE UPDATE ON recordings
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_app_settings_updated_at BEFORE UPDATE ON app_settings
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ============================================================================
-- ROW LEVEL SECURITY (RLS) POLICIES
-- ============================================================================

ALTER TABLE channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE recordings ENABLE ROW LEVEL SECURITY;
ALTER TABLE upload_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tunnels ENABLE ROW LEVEL SECURITY;
ALTER TABLE tunnel_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE recording_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE preview_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE upload_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE disk_usage ENABLE ROW LEVEL SECURITY;

-- Drop existing policies before recreating (safe re-run)
DO $$
DECLARE
    pol RECORD;
BEGIN
    FOR pol IN
        SELECT policyname, tablename
        FROM pg_policies
        WHERE schemaname = 'public'
          AND tablename IN ('channels', 'recordings', 'upload_links', 'app_settings',
                            'tunnels', 'tunnel_sessions', 'channel_logs',
                            'recording_sessions', 'preview_images', 'upload_journal', 'disk_usage')
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', pol.policyname, pol.tablename);
    END LOOP;
END $$;

CREATE POLICY "Allow all operations on channels" ON channels
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on recordings" ON recordings
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on upload_links" ON upload_links
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on app_settings" ON app_settings
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on tunnels" ON tunnels
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on tunnel_sessions" ON tunnel_sessions
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on channel_logs" ON channel_logs
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on recording_sessions" ON recording_sessions
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on preview_images" ON preview_images
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on upload_journal" ON upload_journal
    FOR ALL USING (true) WITH CHECK (true);
CREATE POLICY "Allow all operations on disk_usage" ON disk_usage
    FOR ALL USING (true) WITH CHECK (true);

-- ============================================================================
-- VIEWS
-- ============================================================================
-- Use SECURITY INVOKER so views respect the querying user's RLS policies.

CREATE OR REPLACE VIEW recordings_with_links WITH (security_invoker = true) AS
SELECT 
    r.*,
    COALESCE(
        json_object_agg(ul.host, ul.url) FILTER (WHERE ul.host IS NOT NULL),
        '{}'::json
    ) AS links
FROM recordings r
LEFT JOIN upload_links ul ON r.id = ul.recording_id
GROUP BY r.id;

CREATE OR REPLACE VIEW channel_statistics WITH (security_invoker = true) AS
SELECT 
    c.username,
    c.is_paused,
    COUNT(r.id) AS total_recordings,
    SUM(r.filesize) AS total_filesize_bytes,
    MAX(r.timestamp) AS last_recording_at,
    AVG(r.viewers) AS avg_viewers,
    c.created_at,
    c.updated_at
FROM channels c
LEFT JOIN recordings r ON c.username = r.username
GROUP BY c.id, c.username, c.is_paused, c.created_at, c.updated_at;

CREATE OR REPLACE VIEW recent_activity WITH (security_invoker = true) AS
SELECT 
    'recording' AS activity_type,
    r.username,
    r.filename AS description,
    r.timestamp AS activity_time
FROM recordings r
UNION ALL
SELECT 
    'log' AS activity_type,
    cl.username,
    cl.message AS description,
    cl.created_at AS activity_time
FROM channel_logs cl
ORDER BY activity_time DESC
LIMIT 100;

-- ============================================================================
-- MULTI-INSTANCE MIGRATION
-- ============================================================================
-- If upgrading from a single-instance setup, copy the existing "channels" blob
-- to "channels_default" so the existing instance continues to work.
INSERT INTO app_settings (key, value)
SELECT 'channels_default', value FROM app_settings WHERE key = 'channels'
ON CONFLICT (key) DO NOTHING;

-- Ensure healthcheck entry exists
INSERT INTO app_settings (key, value)
VALUES ('__healthcheck__', '{"status": "ok"}'::jsonb)
ON CONFLICT (key) DO NOTHING;

-- ============================================================================
-- PERMISSIONS
-- ============================================================================

GRANT USAGE ON SCHEMA public TO anon;
GRANT CREATE ON SCHEMA public TO anon;

GRANT ALL ON ALL TABLES IN SCHEMA public TO anon;
ALTER TABLE public.channels OWNER TO anon;
ALTER TABLE public.recordings OWNER TO anon;
ALTER TABLE public.upload_links OWNER TO anon;
ALTER TABLE public.app_settings OWNER TO anon;
ALTER TABLE public.tunnels OWNER TO anon;
ALTER TABLE public.tunnel_sessions OWNER TO anon;
ALTER TABLE public.channel_logs OWNER TO anon;
ALTER TABLE public.recording_sessions OWNER TO anon;
ALTER TABLE public.preview_images OWNER TO anon;
ALTER TABLE public.upload_journal OWNER TO anon;
ALTER TABLE public.disk_usage OWNER TO anon;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO anon;

GRANT SELECT ON public.recordings_with_links TO anon;
GRANT SELECT ON public.channel_statistics TO anon;
GRANT SELECT ON public.recent_activity TO anon;

ALTER VIEW public.recordings_with_links OWNER TO anon;
ALTER VIEW public.channel_statistics OWNER TO anon;
ALTER VIEW public.recent_activity OWNER TO anon;
