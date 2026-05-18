package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

// InitDB initialises the PostgreSQL connection using DATABASE_URL.
// Falls back silently if DATABASE_URL is not set.
func InitDB() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Println(" INFO [db] DATABASE_URL not set — persistence will use local files only")
		return nil
	}

	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)

	if err := conn.Ping(); err != nil {
		conn.Close()
		return fmt.Errorf("ping db: %w", err)
	}

	db = conn

	// Ensure schema exists
	if err := migrate(db); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	fmt.Println(" INFO [db] connected to PostgreSQL — all data will be persisted remotely")
	return nil
}

// migrate creates all required tables if they don't already exist.
func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS channels (
			id SERIAL PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			site TEXT NOT NULL DEFAULT 'chaturbate',
			is_paused BOOLEAN NOT NULL DEFAULT false,
			framerate INTEGER NOT NULL DEFAULT 30,
			resolution INTEGER NOT NULL DEFAULT 1080,
			pattern TEXT NOT NULL DEFAULT '',
			max_duration INTEGER NOT NULL DEFAULT 30,
			max_filesize INTEGER NOT NULL DEFAULT 0,
			compress BOOLEAN NOT NULL DEFAULT false,
			created_at BIGINT NOT NULL DEFAULT 0,
			streamed_at BIGINT,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value JSONB NOT NULL DEFAULT '{}',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS video_uploads (
			id SERIAL PRIMARY KEY,
			streamer_name TEXT NOT NULL,
			filename TEXT,
			gofile_link TEXT,
			turboviplay_link TEXT,
			voesx_link TEXT,
			streamtape_link TEXT,
			byse_link TEXT,
			sendcm_link TEXT,
			thumbnail_link TEXT,
			sprite_link TEXT,
			embed_url TEXT,
			filesize BIGINT,
			room_title TEXT,
			tags JSONB DEFAULT '[]',
			viewers INTEGER DEFAULT 0,
			resolution TEXT,
			framerate INTEGER DEFAULT 0,
			recorded_at TIMESTAMPTZ,
			upload_date TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// ─── Channels ────────────────────────────────────────────────────────────────

type channelConfig struct {
	IsPaused    bool   `json:"is_paused"`
	Username    string `json:"username"`
	Framerate   int    `json:"framerate"`
	Resolution  int    `json:"resolution"`
	Pattern     string `json:"pattern"`
	MaxDuration int    `json:"max_duration"`
	MaxFilesize int    `json:"max_filesize"`
	Compress    bool   `json:"compress"`
	CreatedAt   int64  `json:"created_at"`
}

// SaveChannelsToDB upserts the channel list to PostgreSQL.
func SaveChannelsToDB(data []byte) error {
	if db == nil {
		return nil
	}
	var configs []channelConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("parse channels json: %w", err)
	}

	if len(configs) == 0 {
		_, err := db.Exec(`DELETE FROM channels WHERE id >= 0`)
		return err
	}

	for _, c := range configs {
		_, err := db.Exec(`
			INSERT INTO channels (username, site, is_paused, framerate, resolution, pattern, max_duration, max_filesize, compress, created_at, updated_at)
			VALUES ($1, 'chaturbate', $2, $3, $4, $5, $6, $7, $8, $9, NOW())
			ON CONFLICT (username) DO UPDATE SET
				is_paused    = EXCLUDED.is_paused,
				framerate    = EXCLUDED.framerate,
				resolution   = EXCLUDED.resolution,
				pattern      = EXCLUDED.pattern,
				max_duration = EXCLUDED.max_duration,
				max_filesize = EXCLUDED.max_filesize,
				compress     = EXCLUDED.compress,
				updated_at   = NOW()`,
			c.Username, c.IsPaused, c.Framerate, c.Resolution, c.Pattern,
			c.MaxDuration, c.MaxFilesize, c.Compress, c.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("upsert channel %s: %w", c.Username, err)
		}
	}
	return nil
}

// LoadChannelsFromDB fetches channels from PostgreSQL and returns them as
// the JSON format used by the app, or nil if unavailable.
func LoadChannelsFromDB() []byte {
	if db == nil {
		return nil
	}
	rows, err := db.Query(`
		SELECT username, is_paused, framerate, resolution, pattern, max_duration, max_filesize, compress, created_at
		FROM channels ORDER BY created_at ASC`)
	if err != nil {
		fmt.Printf("[WARN] [db] load channels: %v\n", err)
		return nil
	}
	defer rows.Close()

	var configs []channelConfig
	for rows.Next() {
		var c channelConfig
		if err := rows.Scan(&c.Username, &c.IsPaused, &c.Framerate, &c.Resolution,
			&c.Pattern, &c.MaxDuration, &c.MaxFilesize, &c.Compress, &c.CreatedAt); err != nil {
			fmt.Printf("[WARN] [db] scan channel: %v\n", err)
			continue
		}
		configs = append(configs, c)
	}
	if len(configs) == 0 {
		return nil
	}

	b, err := json.Marshal(configs)
	if err != nil {
		return nil
	}
	return b
}

// ─── Settings ────────────────────────────────────────────────────────────────

// SaveSettingsToDB upserts a settings JSON blob into app_settings.
func SaveSettingsToDB(data []byte) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`
		INSERT INTO app_settings (key, value, updated_at)
		VALUES ('dvr_settings', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		data,
	)
	if err != nil {
		fmt.Printf("[WARN] [db] save settings: %v\n", err)
	}
	return nil
}

// LoadSettingsFromDB fetches the settings blob from app_settings.
func LoadSettingsFromDB() []byte {
	if db == nil {
		return nil
	}
	var raw []byte
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key = 'dvr_settings'`).Scan(&raw)
	if err != nil {
		return nil
	}
	return raw
}

// ─── Recordings ──────────────────────────────────────────────────────────────

type recDBShape struct {
	Version  int                      `json:"version"`
	Channels map[string]*recChanShape `json:"channels"`
}

type recChanShape struct {
	Gender     string          `json:"gender"`
	Recordings []recEntryShape `json:"recordings"`
}

type recEntryShape struct {
	Filename     string            `json:"filename"`
	Timestamp    string            `json:"timestamp"`
	RoomTitle    string            `json:"room_title"`
	Tags         []string          `json:"tags"`
	Viewers      int               `json:"viewers"`
	Resolution   string            `json:"resolution"`
	Framerate    int               `json:"framerate"`
	Links        map[string]string `json:"links"`
	ThumbnailURL string            `json:"thumbnail_url"`
	SpriteURL    string            `json:"sprite_url"`
	EmbedURL     string            `json:"embed_url"`
	Filesize     int64             `json:"filesize"`
}

func SaveRecordingsJSONToDB(data []byte) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`
		INSERT INTO app_settings (key, value, updated_at)
		VALUES ('recordings_db', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		data,
	)
	if err != nil {
		return fmt.Errorf("save recordings json: %w", err)
	}
	return nil
}

// SaveRecordingsToDB syncs the recordings JSON blob to PostgreSQL.
func SaveRecordingsToDB(data []byte) error {
	if db == nil {
		return nil
	}
	return SaveRecordingsJSONToDB(data)
}

func LoadRecordingsJSONFromDB() []byte {
	if db == nil {
		return nil
	}
	var raw []byte
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key = 'recordings_db'`).Scan(&raw)
	if err != nil {
		return nil
	}
	return raw
}

// LoadRecordingsFromDB fetches the recordings JSON blob from PostgreSQL.
func LoadRecordingsFromDB() []byte {
	if db == nil {
		return nil
	}
	return LoadRecordingsJSONFromDB()
}

// ─── Tunnels ──────────────────────────────────────────────────────────────────

func SaveTunnelToDB(url string, runID int) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`
		INSERT INTO app_settings (key, value, updated_at)
		VALUES ('tunnel_url', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		fmt.Sprintf(`"%s"`, url),
	)
	return err
}

func LoadCurrentTunnel() (string, error) {
	if db == nil {
		return "", nil
	}
	var raw []byte
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key = 'tunnel_url'`).Scan(&raw)
	if err != nil {
		return "", nil
	}
	var tunnelURL string
	if err := json.Unmarshal(raw, &tunnelURL); err != nil {
		return "", nil
	}
	return tunnelURL, nil
}
