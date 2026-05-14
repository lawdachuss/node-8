-- Create channels table to store channel configurations
CREATE TABLE IF NOT EXISTS channels (
    id SERIAL PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    site TEXT NOT NULL DEFAULT 'chaturbate',
    is_paused BOOLEAN NOT NULL DEFAULT false,
    framerate INTEGER NOT NULL DEFAULT 30,
    resolution INTEGER NOT NULL DEFAULT 1080,
    pattern TEXT NOT NULL,
    max_duration INTEGER NOT NULL DEFAULT 30,
    max_filesize INTEGER NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    streamed_at BIGINT,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Create index on username for faster lookups
CREATE INDEX IF NOT EXISTS idx_channels_username ON channels(username);

-- Create index on site
CREATE INDEX IF NOT EXISTS idx_channels_site ON channels(site);

-- Enable Row Level Security
ALTER TABLE channels ENABLE ROW LEVEL SECURITY;

-- Create policy to allow all operations (adjust based on your security needs)
CREATE POLICY "Allow all operations on channels" ON channels
    FOR ALL
    USING (true)
    WITH CHECK (true);

-- Add comment
COMMENT ON TABLE channels IS 'Stores channel configurations for the recorder';
