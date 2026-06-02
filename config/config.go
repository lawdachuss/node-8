package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/urfave/cli/v2"
)

var (
	ffmpegPath       string
	autoDetectedFF   string
	autoDetectedOnce sync.Once

	// ffmpegSem limits concurrent ffmpeg processes across all channels.
	// Default: NumCPU * 2 — enough for quick I/O-bound operations (mux,
	// thumbnail) while still throttling CPU-bound compression.  Override
	// with the FFMPEG_CONCURRENCY env var.
	ffmpegSem chan struct{}
)

func init() {
	n := runtime.NumCPU() * 2
	if n < 1 {
		n = 1
	}
	ffmpegSem = make(chan struct{}, n)
}

// SetFFmpegPath sets a custom path for the ffmpeg binary.
func SetFFmpegPath(path string) {
	ffmpegPath = path
}

// autoDetectFFmpeg searches common ffmpeg install locations when PATH lookup
// fails. Runs once and caches the result.
func autoDetectFFmpeg() string {
	autoDetectedOnce.Do(func() {
		// Try PATH lookup first.
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			autoDetectedFF = p
			return
		}

		candidates := []string{
			// WinGet shim directory
		}

		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData != "" {
			candidates = append(candidates,
				filepath.Join(localAppData, "Microsoft", "WinGet", "Links", "ffmpeg.exe"),
			)
			// WinGet packages directory with version glob
			wgDir := filepath.Join(localAppData, "Microsoft", "WinGet", "Packages")
			if entries, err := os.ReadDir(wgDir); err == nil {
				for _, e := range entries {
					if matched, _ := filepath.Match("Gyan.FFmpeg.Essentials*", e.Name()); matched {
						candidates = append(candidates,
							filepath.Join(wgDir, e.Name(), "bin", "ffmpeg.exe"),
						)
					}
				}
			}
		}

		candidates = append(candidates,
			`C:\ProgramData\chocolatey\bin\ffmpeg.exe`,
			`C:\Program Files\FFmpeg\bin\ffmpeg.exe`,
			`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
		)

		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				autoDetectedFF = c
				return
			}
		}
	})
	return autoDetectedFF
}

// ffmpegBin returns the configured ffmpeg path, auto-detected path, or
// "ffmpeg" as final fallback (which relies on PATH lookup by exec.Command).
func ffmpegBin() string {
	if ffmpegPath != "" {
		return ffmpegPath
	}
	if p := autoDetectFFmpeg(); p != "" {
		return p
	}
	return "ffmpeg"
}

// ffprobeBin returns the configured ffprobe path by deriving it from ffmpeg
// path, or "ffprobe" (or "ffprobe.exe" on Windows) as fallback.
func ffprobeBin() string {
	if ffmpegPath != "" {
		dir := filepath.Dir(ffmpegPath)
		return filepath.Join(dir, "ffprobe")
	}
	// If auto-detected, derive ffprobe from the same directory
	if p := autoDetectFFmpeg(); p != "" {
		dir := filepath.Dir(p)
		return filepath.Join(dir, "ffprobe")
	}
	if runtime.GOOS == "windows" {
		return "ffprobe.exe"
	}
	return "ffprobe"
}

// FFmpegCommand returns an exec.Cmd that runs ffmpeg with the given arguments.
func FFmpegCommand(args ...string) *exec.Cmd {
	return exec.Command(ffmpegBin(), args...)
}

// FFmpegCommandContext is like FFmpegCommand but with a context.
func FFmpegCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, ffmpegBin(), args...)
}

// FFprobeCommand returns an exec.Cmd that runs ffprobe with the given arguments.
func FFprobeCommand(args ...string) *exec.Cmd {
	return exec.Command(ffprobeBin(), args...)
}

// FFprobeCommandContext is like FFprobeCommand but with a context.
func FFprobeCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, ffprobeBin(), args...)
}

// AcquireFFmpeg blocks until a ffmpeg slot is available.
func AcquireFFmpeg() {
	ffmpegSem <- struct{}{}
}

// ReleaseFFmpeg releases a ffmpeg slot.
func ReleaseFFmpeg() {
	<-ffmpegSem
}

func New(c *cli.Context) (*entity.Config, error) {
	compress := c.Bool("compress")

	cfg := &entity.Config{
		Version:                c.App.Version,
		Username:               c.String("username"),
		AdminUsername:          c.String("admin-username"),
		AdminPassword:          c.String("admin-password"),
		Framerate:              c.Int("framerate"),
		Resolution:             c.Int("resolution"),
		Pattern:                c.String("pattern"),
		MaxDuration:            c.Int("max-duration"),
		MaxFilesize:            c.Int("max-filesize"),
		Compress:               compress,
		Port:                   c.String("port"),
		Interval:               c.Int("interval"),
		Cookies:                c.String("cookies"),
		UserAgent:              c.String("user-agent"),
		Domain:                 c.String("domain"),
		ProxyURL:               c.String("proxy-url"),
		ProxyUsername:          c.String("proxy-username"),
		ProxyPassword:          c.String("proxy-password"),
		OutputDir:              c.String("output-dir"),
		PerModelFolder:         c.Bool("per-model-folder"),
		DeleteLocalAfterUpload: c.Bool("delete-local-after-upload"),
		OrphanCleanupInterval:  c.Int("orphan-cleanup-interval"),
		DiskWarningPercent:     c.Int("disk-warning-percent"),
		DiskCriticalPercent:    c.Int("disk-critical-percent"),
		MaxLocalAgeDays:        c.Int("max-local-age-days"),
		TurboViPlayAPIKey:      c.String("turboviplay-api-key"),
		VoeSXAPIKey:            c.String("voesx-api-key"),
		SendCMAPIKey:           c.String("sendcm-api-key"),
		ByseAPIKey:             c.String("byse-api-key"),
		StreamtapeLogin:        c.String("streamtape-login"),
		StreamtapeKey:          c.String("streamtape-key"),
		MixdropEmail:           c.String("mixdrop-email"),
		MixdropToken:           c.String("mixdrop-token"),
		PixelDrainToken:        c.String("pixeldrain-token"),
		GitHubToken:            c.String("github-token"),
		GitHubRepo:             c.String("github-repo"),
		GitHubBranch:           c.String("github-branch"),
		GitHubPreviewPath:      c.String("github-preview-path"),
		SupabaseURL:            c.String("supabase-url"),
		SupabaseAPIKey:         c.String("supabase-api-key"),
	}

	// If user provided a custom ffmpeg path, set it globally
	if path := c.String("ffmpeg-path"); path != "" {
		cfg.FFmpegPath = path
		SetFFmpegPath(path)
	}

	return cfg, nil
}
