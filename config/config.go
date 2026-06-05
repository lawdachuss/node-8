package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"time"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/urfave/cli/v2"
)

var (
	ffmpegPath       string
	autoDetectedFF   string
	autoDetectedOnce sync.Once

	// ffmpegSem limits concurrent lightweight ffmpeg/ffprobe processes
	// across all channels: thumbnails, sprite sheets, GIF previews,
	// and A/V muxing. These are I/O-bound and fast, so the pool is
	// generous: NumCPU * 3, minimum 4.
	ffmpegSem chan struct{}

	// ffmpegHeavySem limits concurrent CPU-bound compression (re-encode)
	// across all channels. Only one file per channel is compressed at a
	// time (CompressFile serialises internally), but across N channels
	// we risk thrashing the CPU.  Pool: max(1, NumCPU/2), capped at 4.
	ffmpegHeavySem chan struct{}
)

func init() {
	n := runtime.NumCPU()
	light := n * 3
	if light < 4 {
		light = 4
	}
	ffmpegSem = make(chan struct{}, light)

	heavy := n / 2
	if heavy < 1 {
		heavy = 1
	}
	if heavy > 4 {
		heavy = 4
	}
	ffmpegHeavySem = make(chan struct{}, heavy)
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
	probeName := "ffprobe"
	if runtime.GOOS == "windows" {
		probeName = "ffprobe.exe"
	}
	if ffmpegPath != "" {
		dir := filepath.Dir(ffmpegPath)
		return filepath.Join(dir, probeName)
	}
	// If auto-detected, derive ffprobe from the same directory
	if p := autoDetectFFmpeg(); p != "" {
		dir := filepath.Dir(p)
		return filepath.Join(dir, probeName)
	}
	return probeName
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

// AcquireFFmpeg blocks until a lightweight ffmpeg slot is available.
func AcquireFFmpeg() {
	ffmpegSem <- struct{}{}
}

// ReleaseFFmpeg releases a lightweight ffmpeg slot.
func ReleaseFFmpeg() {
	<-ffmpegSem
}

// AcquireFFmpegHeavy blocks until a CPU-bound compression slot is available.
func AcquireFFmpegHeavy() {
	ffmpegHeavySem <- struct{}{}
}

// ReleaseFFmpegHeavy releases a CPU-bound compression slot.
func ReleaseFFmpegHeavy() {
	<-ffmpegHeavySem
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
		VoeSXAPIKey:            c.String("voesx-api-key"),
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

	if dur := c.String("session-duration"); dur != "" {
		cfg.SessionDuration = dur
		d, err := time.ParseDuration(dur)
		if err == nil && d > 0 {
			cfg.SessionDurationParsed = d
		}
	}

	return cfg, nil
}
