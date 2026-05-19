package channel

import (
        "context"
        "fmt"
        "log"
        "os"
        "os/exec"
        "path/filepath"
        "strconv"
        "strings"
        "time"

        "github.com/teacat/chaturbate-dvr/uploader"
)

const (
        thumbWidth   = 1280
        thumbHeight  = 720
        spriteFrames = 16
        spriteCols   = 4
        spriteRows   = 4
        spriteFrameW = 640
        spriteFrameH = 360
)

// generateThumbnail is the channel-scoped wrapper — logs go to the channel log.
func (ch *Channel) generateThumbnail(videoPath string) (thumbURL, spriteURL string) {
        return generateThumbnailForFile(videoPath,
                func(f string, a ...interface{}) { ch.Info(f, a...) },
                func(f string, a ...interface{}) { ch.Error(f, a...) },
        )
}

// GenerateThumbnailForFile is a standalone thumbnail generator that can be
// called outside of a channel context (e.g. for pre-existing video files).
func GenerateThumbnailForFile(videoPath string) (thumbURL, spriteURL string) {
        return generateThumbnailForFile(videoPath,
                func(f string, a ...interface{}) { log.Printf("[thumb] "+f, a...) },
                func(f string, a ...interface{}) { log.Printf("[thumb:err] "+f, a...) },
        )
}

// generateThumbnailForFile creates a static thumbnail and a multi-frame sprite
// sheet (5 cols × 4 rows = 20 frames covering the full video duration), uploads
// both to remote image hosts, and returns the remote URLs. Always cleans up
// local JPG files.
func generateThumbnailForFile(videoPath string, info, errFn func(string, ...interface{})) (thumbURL, spriteURL string) {
        ext := strings.ToLower(filepath.Ext(videoPath))
        if ext != ".mp4" && ext != ".mkv" && ext != ".ts" {
                return "", ""
        }

        baseName := filepath.Base(videoPath)

        // Probe video duration once — both goroutines need it.
        ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
        defer cancel()

        var dur float64
        probeOut, probeErr := exec.CommandContext(ctx, "ffprobe",
                "-v", "error",
                "-show_entries", "format=duration",
                "-of", "default=noprint_wrappers=1:nokey=1",
                videoPath,
        ).Output()
        if probeErr == nil {
                dur, _ = strconv.ParseFloat(strings.TrimSpace(string(probeOut)), 64)
        }

        thumbDone := make(chan string, 1)
        spriteDone := make(chan string, 1)

        // ── Single thumbnail (static frame near the 10% mark) ──────────────────
        go func() {
                thumbJPG := videoPath + ".thumb.jpg"

                seekPos := "00:00:03"
                if dur > 0 && dur < 3 {
                        seekPos = fmt.Sprintf("%.2f", dur*0.5)
                } else if dur > 0 {
                        seekPos = fmt.Sprintf("%.2f", dur*0.1)
                }

                err := exec.CommandContext(ctx, "ffmpeg",
                        "-y",
                        "-ss", seekPos,
                        "-i", videoPath,
                        "-vframes", "1",
                        "-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
                                thumbWidth, thumbHeight, thumbWidth, thumbHeight),
                        "-q:v", "2",
                        thumbJPG,
                ).Run()

                if err != nil {
                        errFn("thumb: failed for %s: %v", baseName, err)
                        thumbDone <- ""
                        return
                }

                imgUploader := uploader.NewMultiImageUploader()
                if remoteURL, _, uploadErr := imgUploader.Upload(thumbJPG); uploadErr == nil {
                        info("thumb: ✓ %s", baseName)
                        thumbDone <- remoteURL
                } else {
                        errFn("thumb: upload failed for %s: %v", baseName, uploadErr)
                        thumbDone <- ""
                }
        }()

        // ── Sprite sheet (4×4 grid covering the full video duration) ───────────
        // Each frame is spriteFrameW×spriteFrameH px; total image is
        // (spriteCols*spriteFrameW) × (spriteRows*spriteFrameH) = 2560×1440.
        // Using 640×360 frames so HiDPI/Retina displays get sharp previews.
        go func() {
                spriteJPG := videoPath + ".sprite.jpg"

                // Compute the interval so we get exactly spriteFrames frames spread
                // evenly across the whole video.  Clamp to at least 0.1 s.
                interval := 10.0 // default fallback: one frame every 10 s
                if dur > 0 {
                        interval = dur / float64(spriteFrames)
                        if interval < 0.1 {
                                interval = 0.1
                        }
                }

                // fps=1/INTERVAL extracts one frame per interval.
                // scale with lanczos gives sharper results than the default bilinear.
                // pad keeps each tile at exactly spriteFrameW×spriteFrameH.
                // tile=COLSxROWS assembles them into the contact sheet.
                vf := fmt.Sprintf(
                        "fps=1/%.4f,scale=%d:%d:force_original_aspect_ratio=decrease:flags=lanczos,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,tile=%dx%d",
                        interval,
                        spriteFrameW, spriteFrameH,
                        spriteFrameW, spriteFrameH,
                        spriteCols, spriteRows,
                )

                err := exec.CommandContext(ctx, "ffmpeg",
                        "-y",
                        "-i", videoPath,
                        "-vf", vf,
                        "-frames:v", "1",
                        "-q:v", "2",
                        spriteJPG,
                ).Run()

                if err != nil {
                        errFn("sprite: failed for %s: %v", baseName, err)
                        spriteDone <- ""
                        return
                }

                imgUploader := uploader.NewMultiImageUploader()
                if remoteURL, _, uploadErr := imgUploader.Upload(spriteJPG); uploadErr == nil {
                        info("sprite: ✓ %s", baseName)
                        spriteDone <- remoteURL
                } else {
                        errFn("sprite: upload failed for %s: %v", baseName, uploadErr)
                        spriteDone <- ""
                }
        }()

        thumbURL = <-thumbDone
        spriteURL = <-spriteDone

        os.Remove(videoPath + ".thumb.jpg")
        os.Remove(videoPath + ".sprite.jpg")

        return thumbURL, spriteURL
}
