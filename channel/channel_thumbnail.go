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

        "github.com/teacat/chaturbate-dvr/server"
        "github.com/teacat/chaturbate-dvr/uploader"
)

const numSpriteFrames = 10
const spriteFrameWidth = 640
const spriteFrameHeight = 360

// generateThumbnail is the channel-scoped wrapper — logs go to the channel log.
func (ch *Channel) generateThumbnail(videoPath string) {
        generateThumbnailForFile(videoPath,
                func(f string, a ...interface{}) { ch.Info(f, a...) },
                func(f string, a ...interface{}) { ch.Error(f, a...) },
        )
}

// GenerateThumbnailForFile is a standalone thumbnail generator that can be
// called outside of a channel context (e.g. for pre-existing video files).
func GenerateThumbnailForFile(videoPath string) {
        generateThumbnailForFile(videoPath,
                func(f string, a ...interface{}) { log.Printf("[thumb] "+f, a...) },
                func(f string, a ...interface{}) { log.Printf("[thumb:err] "+f, a...) },
        )
}

// generateThumbnailForFile extracts a thumbnail and a sprite sheet of 10
// evenly-spaced frames from the video and saves them as local .thumb.jpg /
// .sprite.jpg files alongside the recording. No external upload is needed.
// URLs are saved as sidecars: video.mp4.thumb and video.mp4.sprite
func generateThumbnailForFile(videoPath string, info, errFn func(string, ...interface{})) {
        ext := strings.ToLower(filepath.Ext(videoPath))
        if ext != ".mp4" && ext != ".mkv" {
                return
        }

        baseName := filepath.Base(videoPath)

	// ── 1. Thumbnail frame (adaptive timestamp) ──────────────────────────────
	thumbSidecar := videoPath + ".thumb"
	thumbJPG := videoPath + ".thumb.jpg"
	if _, err := os.Stat(thumbSidecar); os.IsNotExist(err) {
		// Probe duration to pick a safe thumbnail timestamp
		thumbTime := "00:00:05"
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if out, e := exec.CommandContext(probeCtx, "ffprobe",
			"-v", "error",
			"-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1",
			videoPath,
		).Output(); e == nil {
			if d, e := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); e == nil && d > 0 {
				if d < 5 {
					thumbTime = fmt.Sprintf("%.1f", d/2)
				}
			}
		}
		probeCancel()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := exec.CommandContext(ctx, "ffmpeg",
			"-y", "-i", videoPath,
			"-ss", thumbTime,
			"-vframes", "1",
			"-s", fmt.Sprintf("%dx%d", spriteFrameWidth, spriteFrameHeight),
			"-q:v", "2",
			thumbJPG,
		).Run()
		cancel()

                if err != nil {
                        info("thumb: frame extract failed for %s: %v", baseName, err)
                } else if _, statErr := os.Stat(thumbJPG); statErr == nil {
                        imgUploader := uploader.NewMultiImageUploader()
                        if remoteURL, _, uploadErr := imgUploader.Upload(thumbJPG); uploadErr == nil {
                                if writeErr := os.WriteFile(thumbSidecar, []byte(remoteURL), 0644); writeErr == nil {
                                        info("thumb: uploaded thumbnail for %s to remote host", baseName)
                                } else {
                                        errFn("thumb: could not write sidecar for %s: %v", baseName, writeErr)
                                }
                        } else {
                                info("thumb: remote upload failed for %s, using local fallback: %v", baseName, uploadErr)
                                localURL := "/thumb?path=" + videoPath
                                if writeErr := os.WriteFile(thumbSidecar, []byte(localURL), 0644); writeErr == nil {
                                        info("thumb: saved local thumbnail for %s", baseName)
                                } else {
                                        errFn("thumb: could not write sidecar for %s: %v", baseName, writeErr)
                                }
                        }
                        if server.Config != nil && server.Config.DeleteLocalAfterUpload {
                                _ = os.Remove(thumbJPG)
                        }
                }
        }

	spriteSidecar := videoPath + ".sprite"
	spriteJPG := videoPath + ".sprite.jpg"
	if _, err := os.Stat(spriteSidecar); os.IsNotExist(err) {
		duration := 30.0
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if out, e := exec.CommandContext(probeCtx, "ffprobe",
			"-v", "error",
			"-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1",
			videoPath,
		).Output(); e == nil {
			if d, e := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); e == nil && d > 1 {
				duration = d
			}
		}
		probeCancel()

		// Reduce frame count for short videos to avoid extracting duplicate frames
		framesToExtract := numSpriteFrames
		if duration < 30 {
			framesToExtract = 6
		}
		if duration < 10 {
			framesToExtract = 3
		}
		if duration < 5 {
			info("thumb: skipping sprite for %s (video too short: %.1fs)", baseName, duration)
			return
		}

		tmpDir := videoPath + ".sprite_frames"
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			errFn("thumb: could not create temp dir for %s: %v", baseName, err)
			return
		}
		interval := duration / float64(framesToExtract)
		allOK := true

		for i := 0; i < framesToExtract && allOK; i++ {
			seek := float64(i) * interval
			framePath := filepath.Join(tmpDir, fmt.Sprintf("f_%02d.png", i))
			frameCtx, frameCancel := context.WithTimeout(context.Background(), 30*time.Second)
			if out, e := exec.CommandContext(frameCtx, "ffmpeg",
				"-y",
				"-ss", fmt.Sprintf("%.1f", seek),
				"-i", videoPath,
				"-vframes", "1",
				"-s", fmt.Sprintf("%dx%d", spriteFrameWidth, spriteFrameHeight),
				framePath,
			).CombinedOutput(); e != nil {
				info("thumb: sprite frame %d/%d failed for %s: %v", i+1, framesToExtract, baseName, e)
				if len(out) > 0 {
					msg := string(out)
					if len(msg) > 300 {
						msg = msg[:300]
					}
					info("thumb: ffmpeg output: %s", msg)
				}
				allOK = false
			}
			frameCancel()
		}

		if allOK {
			args := []string{"-y"}
			for i := 0; i < framesToExtract; i++ {
				args = append(args, "-i", filepath.Join(tmpDir, fmt.Sprintf("f_%02d.png", i)))
			}
			args = append(args,
				"-filter_complex", fmt.Sprintf("hstack=inputs=%d", framesToExtract),
				"-frames:v", "1",
				"-q:v", "2",
				spriteJPG,
			)

			tileCtx, tileCancel := context.WithTimeout(context.Background(), 30*time.Second)
			if out, e := exec.CommandContext(tileCtx, "ffmpeg", args...).CombinedOutput(); e != nil {
				tileCancel()
				info("thumb: sprite tile failed for %s: %v", baseName, e)
				if len(out) > 0 {
					msg := string(out)
					if len(msg) > 300 {
						msg = msg[:300]
					}
					info("thumb: ffmpeg output: %s", msg)
				}
			} else {
				tileCancel()
				if _, e := os.Stat(spriteJPG); e == nil {
					imgUploader := uploader.NewMultiImageUploader()
					if remoteURL, _, uploadErr := imgUploader.Upload(spriteJPG); uploadErr == nil {
						if writeErr := os.WriteFile(spriteSidecar, []byte(remoteURL), 0644); writeErr == nil {
							info("thumb: uploaded sprite for %s to remote host", baseName)
						} else {
							errFn("thumb: could not write sprite sidecar for %s: %v", baseName, writeErr)
						}
					} else {
						info("thumb: remote sprite upload failed for %s, using local fallback: %v", baseName, uploadErr)
						localURL := "/sprite?path=" + videoPath
						if writeErr := os.WriteFile(spriteSidecar, []byte(localURL), 0644); writeErr == nil {
							info("thumb: saved local sprite for %s", baseName)
						} else {
							errFn("thumb: could not write sprite sidecar for %s: %v", baseName, writeErr)
						}
					}
					if server.Config != nil && server.Config.DeleteLocalAfterUpload {
						_ = os.Remove(spriteJPG)
					}
				}
			}
		}
		_ = os.RemoveAll(tmpDir)
	}
}
