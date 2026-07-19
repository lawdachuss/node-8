package channel

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/teacat/chaturbate-dvr/config"
	"github.com/teacat/chaturbate-dvr/uploader"
)

const (
	thumbWidth      = 1280
	thumbHeight     = 720
	previewWidth    = 320
	previewHeight   = 180 // 16:9 of previewWidth — fixed so concat never fails on aspect drift
	previewDuration = 18.0 // total seconds of the stitched montage
	previewSegments = 12   // number of smooth clips to stitch (each ~1.5s)

	// Seekbar-grade sprite settings (industry standard, like YouTube/Netflix)
	// Generates frames at computed intervals, 10×10 tiles per sheet (100 frames/sheet)
	spriteTileCols  = 10
	spriteTileRows  = 10
	spritePerSheet  = 100 // 10 × 10
	spriteFrameW    = 160 // standard seekbar preview width
	spriteFrameH    = 90  // 16:9
	spriteQuality   = 5   // JPEG quality (lower=better, 2–5 recommended)
	spriteTargetMin = 50  // minimum target frames for short videos
	spriteTargetMax = 800 // maximum frames to generate
)

// generateThumbnail is the channel-scoped wrapper — logs go to the channel log.
func (ch *Channel) generateThumbnail(videoPath string) (thumbURL, spriteURL, previewURL, spriteVTTURL string) {
	return generateThumbnailForFile(videoPath,
		func(f string, a ...interface{}) { ch.Info(f, a...) },
		func(f string, a ...interface{}) { ch.Error(f, a...) },
	)
}

// GenerateThumbnailForFile is a standalone thumbnail generator that can be
// called outside of a channel context (e.g. for pre-existing video files).
func GenerateThumbnailForFile(videoPath string) (thumbURL, spriteURL, previewURL, spriteVTTURL string) {
	return generateThumbnailForFile(videoPath,
		func(f string, a ...interface{}) { log.Printf("[thumb] "+f, a...) },
		func(f string, a ...interface{}) { log.Printf("[thumb:err] "+f, a...) },
	)
}

// generateThumbnailForFile creates a static thumbnail (JPEG), a multi-frame sprite
// sheet (JPEG), and an MP4 hover preview (6 seconds of smooth clips from
// across the full video).  All three are uploaded to remote hosts and the
// URLs returned.  Local temp files are always cleaned up.
//
// JPEG is used for thumbnail and sprite because:
//   - All image hosts support it (Pixhost, Catbox)
//   - mjpeg encoder is fast (minimal encoding lag)
//   - Small filesize with good visual quality
//
// MP4 is used for the animated preview because:
//   - ~90% smaller than GIF at same quality
//   - Full 24-bit color (no 256-color palette limit)
//   - Smooth native-framerate playback (GIF was variable ~1-8fps)
//   - Catbox accepts MP4 files (free, permanent, CDN-backed)
//
// The preview uses filter_complex to extract 12 short clips (~1.5s each)
// spanning the full duration — anchored at the start and end of the stream
// with the rest evenly spaced 0%..100% — and stitches them together.
// Each clip has consecutive frames for fully smooth motion, unlike a
// frame-sampled timelapse where every frame is a jarring jump.
//
// Thumbnail, sprite, and preview run in parallel with independent timeouts:
//   - thumbnail: 5 min  (single-frame seek)
//   - sprite:    15 min (16 input seeks — instant per frame, not full decode)
//   - preview:   15 min (12 input seeks × 1.5s clip, not full decode)
//
// All three use input seeking (-ss before -i) so ffmpeg jumps to the target
// time and reads only what it needs — a 1-hour recording no longer requires
// decoding the whole file just to make a thumbnail.
//
// Using separate contexts prevents one task from being killed prematurely
// when a long video causes another to exceed a shared short timeout.
// fileExists returns true if the path exists and is a regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// runFFmpeg runs ffmpeg, returning any stderr output (capped to the last 2 KB)
// alongside the error.  ffmpeg writes its diagnostics to stderr, so calling
// .Run() directly only gives the caller "exit status 1" and the real cause
// (missing codec, invalid filter, image2 muxer requiring -update, etc.) — and
// therefore the actual fix — is lost.
func runFFmpeg(ctx context.Context, args ...string) (string, error) {
	cmd := config.FFmpegCommandContext(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		out := stderr.String()
		if len(out) > 2000 {
			out = out[len(out)-2000:]
		}
		return out, err
	}
	return "", nil
}

func generateThumbnailForFile(videoPath string, info, errFn func(string, ...interface{})) (thumbURL, spriteURL, previewURL, spriteVTTURL string) {
	ext := strings.ToLower(filepath.Ext(videoPath))
	if ext != ".mp4" && ext != ".mkv" && ext != ".ts" {
		return "", "", "", ""
	}

	st, err := os.Stat(videoPath)
	if err != nil {
		errFn("thumb: file not found %s: %v", filepath.Base(videoPath), err)
		return "", "", "", ""
	}
	// Skip files too small to contain video frames — ffmpeg returns
	// exit code -22 (EINVAL) on header-only fMP4 from failed streams.
	if st.Size() < 100*1024 {
		errFn("thumb: skipping %s: too small (%d bytes)", filepath.Base(videoPath), st.Size())
		return "", "", "", ""
	}

	baseName := filepath.Base(videoPath)

	// Probe video duration — short dedicated timeout.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer probeCancel()

	var dur float64
	release := config.AcquireFFmpeg()
	probeOut, probeErr := config.FFprobeCommandContext(probeCtx,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		videoPath,
	).Output()
	release() // release immediately — the 3 goroutines below also need slots
	if probeErr == nil {
		var parseErr error
		dur, parseErr = strconv.ParseFloat(strings.TrimSpace(string(probeOut)), 64)
		if parseErr != nil {
			log.Printf("WARN: could not parse probe duration %q: %v", strings.TrimSpace(string(probeOut)), parseErr)
		}
	}

	// Probe the first-frame PTS offset once so the sprite/preview fast paths
	// can seek correctly in inputs that carry absolute timestamps (LL-HLS fMP4).
	// Without it, -ss would seek into the file's real timeline incorrectly.
	var ptsOffset float64
	if dur > 0 {
		ptsOffset = probeFirstPTSOffset(videoPath)
	}

	thumbDone := make(chan string, 1)
	spriteDone := make(chan string, 1)
	previewDone := make(chan string, 1)
	vttDone := make(chan string, 1)

	// ── Single thumbnail (static frame near the 10% mark) ──────────────────
	// Independent 90-second context: seeking to a single frame is always fast.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC [thumb] generating thumbnail for %s: %v", baseName, r)
				select {
				case thumbDone <- "":
				default:
				}
			}
		}()
		thumbCtx, thumbCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer thumbCancel()

		thumbJPG := videoPath + ".thumb.jpg"
		defer os.Remove(thumbJPG)

		seekPos := "00:00:03"
		if dur > 0 && dur < 3 {
			seekPos = fmt.Sprintf("%.2f", dur*0.5)
		} else if dur > 0 {
			seekPos = fmt.Sprintf("%.2f", dur*0.1)
		}

		defer config.AcquireFFmpeg()()
		stderr, err := runFFmpeg(thumbCtx,
			"-y",
			"-ss", seekPos,
			"-i", videoPath,
			"-vframes", "1",
			"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
				thumbWidth, thumbHeight, thumbWidth, thumbHeight),
			"-c:v", "mjpeg",
			"-q:v", "5",
			"-update", "1",
			thumbJPG,
		)

		// Fallback: if the initial seek missed (very short clips, or an
		// unknown duration that fell back to a fixed 00:00:03 seek beyond the
		// end of the file), grab the first available frame instead.
		if err != nil || !fileExists(thumbJPG) {
			if err != nil {
				errFn("thumb: seek failed for %s: %v (ffmpeg: %s) — trying first-frame fallback", baseName, err, stderr)
			} else {
				errFn("thumb: seek produced no output for %s — trying first-frame fallback", baseName)
			}
			fbCtx, fbCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer fbCancel()
			_, fbErr := runFFmpeg(fbCtx,
				"-y",
				"-ss", "0",
				"-i", videoPath,
				"-vframes", "1",
				"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
					thumbWidth, thumbHeight, thumbWidth, thumbHeight),
				"-c:v", "mjpeg",
				"-q:v", "5",
				"-update", "1",
				thumbJPG,
			)
			if fbErr != nil {
				errFn("thumb: fallback also failed for %s: %v", baseName, fbErr)
				thumbDone <- ""
				return
			}
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

	// ── Seekbar sprites (multi-sheet, interval-based, with WebVTT) ────────
	// Professional approach (YouTube/Netflix/Imgix standard):
	//   - Dynamic interval based on video duration (target ~200 frames)
	//   - 10x10 tiles per sheet (100 frames/sheet) at 160x90 each
	//   - Multiple sheets for long videos via sprite_%03d.jpg pattern
	//   - WebVTT file maps timestamps to #xywh coordinates
	//   - Single-pass ffmpeg: fps=1/N,scale=160:-2,tile=10x10
	//   - All sheets + VTT uploaded, URLs stored in DB
	//
	// Interval selection: short videos get dense sampling, long videos get sparse.
	// This keeps the total frame count in a reasonable range (50-800).
	// The 20-minute timeout is generous for 4K long-form content.
	go func() {
		var spriteCancel context.CancelFunc
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC [sprite] generating sprite for %s: %v", baseName, r)
				if spriteCancel != nil {
					spriteCancel()
				}
				select {
				case spriteDone <- "":
				default:
				}
				select {
				case vttDone <- "":
				default:
				}
			}
		}()
		var spriteCtx context.Context
		spriteCtx, spriteCancel = context.WithTimeout(context.Background(), 20*time.Minute)
		defer spriteCancel()

		spriteBase := videoPath + ".sprite"
		defer func() {
			glob, _ := filepath.Glob(spriteBase + "_*.jpg")
			for _, f := range glob {
				os.Remove(f)
			}
			os.Remove(spriteBase + ".vtt")
			os.Remove(spriteBase + ".jpg")
		}()

		// Compute interval: target ~200 frames, clamp to [2s, 120s]
		spriteInterval := 10.0
		if dur > 0 {
			target := dur / 200.0
			if target < 2.0 {
				target = 2.0
			}
			if target > 120.0 {
				target = 120.0
			}
			spriteInterval = target
		}

		// Generate sprite sheets using single-pass ffmpeg.
		// The fps filter samples one frame every spriteInterval seconds;
		// tile arranges into 10x10 grids. Multiple sheets are produced
		// automatically when the frame count exceeds 100.
		defer config.AcquireFFmpeg()()

		spritePattern := spriteBase + "_%03d.jpg"
		vf := fmt.Sprintf("fps=1/%.4f,scale=%d:-2:flags=lanczos,tile=%dx%d",
			spriteInterval, spriteFrameW, spriteTileCols, spriteTileRows)

		stderr, err := runFFmpeg(spriteCtx,
			"-y",
			"-i", videoPath,
			"-vf", vf,
			"-c:v", "mjpeg",
			"-q:v", fmt.Sprintf("%d", spriteQuality),
			spritePattern,
		)

		if err != nil || !fileExists(fmt.Sprintf(spritePattern, 1)) {
			if err != nil {
				errFn("sprite: generation failed for %s: %v (ffmpeg: %s) - trying single-frame fallback", baseName, err, stderr)
			} else {
				errFn("sprite: no sheets produced for %s - trying single-frame fallback", baseName)
			}
			fbCtx, fbCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer fbCancel()
			fbJPG := spriteBase + ".jpg"
			_, fbErr := runFFmpeg(fbCtx,
				"-y",
				"-ss", fmt.Sprintf("%.2f", ptsOffset+dur*0.1),
				"-i", videoPath,
				"-vframes", "1",
				"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
					spriteTileCols*spriteFrameW, spriteTileRows*spriteFrameH,
					spriteTileCols*spriteFrameW, spriteTileRows*spriteFrameH),
				"-c:v", "mjpeg",
				"-q:v", fmt.Sprintf("%d", spriteQuality),
				"-update", "1",
				fbJPG,
			)
			if fbErr != nil {
				errFn("sprite: fallback also failed for %s: %v", baseName, fbErr)
				spriteDone <- ""
				vttDone <- ""
				return
			}
			imgUploader := uploader.NewMultiImageUploader()
			if remoteURL, _, uploadErr := imgUploader.Upload(fbJPG); uploadErr == nil {
				info("sprite: ✓ %s (fallback single frame)", baseName)
				spriteDone <- remoteURL
				vttDone <- ""
			} else {
				errFn("sprite: upload failed for %s: %v", baseName, uploadErr)
				spriteDone <- ""
				vttDone <- ""
			}
			os.Remove(fbJPG)
			return
		}

		// Collect generated sprite sheet files, ordered
		var sheetFiles []string
		for i := 1; ; i++ {
			f := fmt.Sprintf(spritePattern, i)
			if !fileExists(f) {
				break
			}
			sheetFiles = append(sheetFiles, f)
		}

		// Upload all sprite sheets to image hosts, collect remote URLs
		sheetURLs := make([]string, 0, len(sheetFiles))
		imgUploader := uploader.NewMultiImageUploader()
		for _, sf := range sheetFiles {
			remoteURL, _, uploadErr := imgUploader.Upload(sf)
			if uploadErr != nil {
				errFn("sprite: upload failed for sheet %s: %v", filepath.Base(sf), uploadErr)
				continue
			}
			sheetURLs = append(sheetURLs, remoteURL)
		}

		if len(sheetURLs) == 0 {
			errFn("sprite: all sheet uploads failed for %s", baseName)
			spriteDone <- ""
			vttDone <- ""
			return
		}

		// Generate WebVTT file mapping timestamps to sprite coordinates.
		// totalFrames = actual number of frames ffmpeg generated in the tiles.
		// ffmpeg's fps=1/N filter produces ceil(dur/interval) frames, so we
		// round up when dur isn't evenly divisible.
		totalFrames := len(sheetFiles) * spritePerSheet
		if dur > 0 {
			actualFrames := int(dur / spriteInterval)
			if float64(actualFrames)*spriteInterval < dur {
				actualFrames++
			}
			if actualFrames < totalFrames {
				totalFrames = actualFrames
			}
		}

		var vttContent strings.Builder
		vttContent.WriteString("WEBVTT\n\n")
		for i := 0; i < totalFrames; i++ {
			startTime := float64(i) * spriteInterval
			endTime := startTime + spriteInterval
			if dur > 0 && endTime > dur {
				endTime = dur
			}

			sheetIdx := i / spritePerSheet
			if sheetIdx >= len(sheetURLs) {
				break
			}
			localIdx := i % spritePerSheet
			col := localIdx % spriteTileCols
			row := localIdx / spriteTileCols
			x := col * spriteFrameW
			y := row * spriteFrameH

			vttContent.WriteString(fmt.Sprintf("%s --> %s\n",
				formatVTTCueTime(startTime),
				formatVTTCueTime(endTime)))
			vttContent.WriteString(fmt.Sprintf("%s#xywh=%d,%d,%d,%d\n\n",
				sheetURLs[sheetIdx], x, y, spriteFrameW, spriteFrameH))
		}

		// Write VTT to temp file and upload
		vttFile := spriteBase + ".vtt"
		if writeErr := os.WriteFile(vttFile, []byte(vttContent.String()), 0644); writeErr != nil {
			errFn("sprite: failed to write VTT for %s: %v", baseName, writeErr)
			spriteDone <- sheetURLs[0]
			vttDone <- ""
			return
		}

		catboxUploader := uploader.NewCatboxUploader()
		vttURL, vttErr := catboxUploader.Upload(vttFile)
		if vttErr != nil {
			errFn("sprite: VTT upload failed for %s: %v", baseName, vttErr)
		}

		info("sprite: ✓ %s (%d sheets, %d frames, interval=%.1fs)",
			baseName, len(sheetURLs), totalFrames, spriteInterval)
		spriteDone <- sheetURLs[0]
		vttDone <- vttURL
	}()

	// ── MP4 hover preview (smooth clips from across the video, 18s total) ──
	// Uses input seeking (-ss before each -i) with concat filter instead of
	// the select filter approach. This is much faster for long videos because
	// ffmpeg seeks to each clip position rather than decoding the entire file.
	//
	//   <18 sec: plays whole video at normal speed
	//   1 min:   12 clips x 1.5s = 18s (4.5s between clips)
	//   60 min:  12 clips x 1.5s = 18s (5 min between clips)
	//
	// Uploaded to Catbox.moe with LobFile as fallback.
	go func() {
		var previewCancel context.CancelFunc
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC [preview] generating preview for %s: %v", baseName, r)
				if previewCancel != nil {
					previewCancel()
				}
				select {
				case previewDone <- "":
				default:
				}
			}
		}()
		var previewCtx context.Context
		previewCtx, previewCancel = context.WithTimeout(context.Background(), 15*time.Minute)
		defer previewCancel()

		previewMP4 := videoPath + ".preview.mp4"
		var previewGenerated bool
		defer func() {
			if previewGenerated {
				os.Remove(previewMP4)
			}
		}()

		waitForPreviewFile := func() bool {
			for delay := 0; delay < 14; delay++ {
				if fileExists(previewMP4) {
					return true
				}
				time.Sleep(time.Duration(50*(1<<delay)) * time.Millisecond)
			}
			return false
		}

		generatePreview := func(ctx context.Context) bool {
			var err error

			if dur <= 0 || dur <= previewDuration {
				seek := "0"
				if dur <= 0 && ptsOffset > 0 {
					seek = fmt.Sprintf("%.2f", ptsOffset)
				}
				limit := ""
				if dur <= 0 {
					limit = fmt.Sprintf("%.2f", previewDuration)
				}
				args := []string{"-y", "-ss", seek, "-i", videoPath}
				if limit != "" {
					args = append(args, "-t", limit)
				}
				args = append(args,
					"-vf", fmt.Sprintf("scale=%d:-2:flags=lanczos,fps=30", previewWidth),
					"-vsync", "vfr",
					"-c:v", "libx264",
					"-preset", "fast",
					"-crf", "23",
					"-movflags", "+faststart",
					"-an",
					previewMP4,
				)
				_, err = runFFmpeg(ctx, args...)
			} else {
				// Long video: extract clips via input seeking + concat.
				// Each clip is seeked-to independently (-ss before -i),
				// trimmed to segDur, then concatenated.
				segDur := previewDuration / float64(previewSegments)
				clips := previewSegments
				if clips < 2 {
					clips = 2
				}

				// Calculate clip centers anchored at start and end
				type clipRange struct{ start, end float64 }
				ranges := make([]clipRange, clips)
				for i := 0; i < clips; i++ {
					center := (float64(i) / float64(clips-1)) * dur
					start := center - segDur/2
					if start < 0 {
						start = 0
					}
					end := start + segDur
					if end > dur {
						start = dur - segDur
						end = dur
					}
					if start < 0 {
						start = 0
					}
					ranges[i] = clipRange{start, end}
				}

				args := []string{"-y"}
				for _, r := range ranges {
					seek := fmt.Sprintf("%.3f", ptsOffset+r.start)
					args = append(args, "-ss", seek, "-i", videoPath)
				}

				// Build filter_complex: trim each input, setpts, concat
				var filterParts []string
				var concatIns []string
				for i := 0; i < clips; i++ {
					dur := ranges[i].end - ranges[i].start
					filterParts = append(filterParts, fmt.Sprintf(
						"[%d:v]trim=0:%.3f,setpts=PTS-STARTPTS,scale=%d:-2:flags=lanczos,fps=30[s%d]",
						i, dur, previewWidth, i))
					concatIns = append(concatIns, fmt.Sprintf("[s%d]", i))
				}
				filterParts = append(filterParts,
					fmt.Sprintf("%sconcat=n=%d:v=1:a=0[v]", strings.Join(concatIns, ""), clips))

				args = append(args,
					"-filter_complex", strings.Join(filterParts, ";"),
					"-map", "[v]",
					"-c:v", "libx264",
					"-preset", "fast",
					"-crf", "23",
					"-movflags", "+faststart",
					"-an",
					previewMP4,
				)
				_, err = runFFmpeg(ctx, args...)
			}

			if err != nil {
				errFn("preview: ffmpeg failed for %s: %v", baseName, err)
				return false
			}

			if !waitForPreviewFile() {
				errFn("preview: ffmpeg exited successfully but produced no output file for %s", baseName)
				return false
			}

			return true
		}

		release := config.AcquireFFmpeg()
		previewOK := generatePreview(previewCtx)
		release()
		if !previewOK {
			previewDone <- ""
			return
		}
		previewGenerated = true

		catboxUploader := uploader.NewCatboxUploader()
		lobfileUploader := uploader.NewLobFileUploader(os.Getenv("LOBFILE_API_KEY"))
		var remoteURL string
		var uploadErr error

		maxPreviewAttempts := 2
		for attempt := 0; attempt < maxPreviewAttempts; attempt++ {
			if attempt > 0 {
				info("preview: regenerating %s (attempt %d/%d)", baseName, attempt+1, maxPreviewAttempts)
				release := config.AcquireFFmpeg()
				regenCtx, regenCancel := context.WithTimeout(context.Background(), 5*time.Minute)
				ok := generatePreview(regenCtx)
				regenCancel()
				release()
				if !ok {
					uploadErr = fmt.Errorf("preview regeneration failed")
					break
				}
			}

			remoteURL, uploadErr = catboxUploader.Upload(previewMP4)
			if uploadErr == nil {
				break
			}
			errFn("preview: catbox failed for %s: %v, trying LobFile", baseName, uploadErr)

			remoteURL, uploadErr = lobfileUploader.Upload(previewMP4)
			if uploadErr == nil {
				break
			}
			errFn("preview: LobFile failed for %s: %v", baseName, uploadErr)

			if os.IsNotExist(uploadErr) || os.IsPermission(uploadErr) || strings.HasSuffix(uploadErr.Error(), "no such file or directory") {
				continue
			}

			break
		}

		if uploadErr == nil {
			info("preview: ✓ %s", baseName)
			previewDone <- remoteURL
		} else {
			errFn("preview: Catbox and LobFile both failed for %s: %v", baseName, uploadErr)
			previewDone <- ""
		}
	}()

	// Hard overall deadline so a stuck sub-step (a blocked ffmpeg semaphore
	// slot, a hung upload, a wedged ffmpeg) can never hang the caller forever.
	// The old code waited on the three done-channels with no timeout, which is
	// exactly the "shows a little preview then got stuck" hang. We wait up to
	// 15 minutes and return whatever completed; the goroutines self-terminate
	// via their own contexts.
	overallCtx, overallCancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer overallCancel()

	recv := func(ch chan string) string {
		select {
		case v := <-ch:
			return v
		case <-overallCtx.Done():
			return ""
		}
	}

	thumbURL = recv(thumbDone)
	spriteURL = recv(spriteDone)
	previewURL = recv(previewDone)
	spriteVTTURL = recv(vttDone)

	if thumbURL == "" || spriteURL == "" || previewURL == "" {
		errFn("thumb: generation incomplete (thumb=%q sprite=%q preview=%q) — returning partial result", thumbURL, spriteURL, previewURL)
	}

	return thumbURL, spriteURL, previewURL, spriteVTTURL
}

// formatVTTCueTime formats seconds as HH:MM:SS.mmm for WebVTT cue timings.
func formatVTTCueTime(sec float64) string {
	h := int(sec) / 3600
	m := (int(sec) % 3600) / 60
	s := int(sec) % 60
	ms := int((sec - float64(int(sec))) * 1000)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

// probeFirstPTSOffset returns the PTS of the first video frame, or 0 if it
// cannot be determined.  LL-HLS fMP4 segments may carry absolute server
// timestamps (e.g. starting at 5044s), which causes trim=start=X to select
// wrong frames since trim uses PTS values.
func probeFirstPTSOffset(videoPath string) float64 {
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer probeCancel()
	defer config.AcquireFFmpeg()()
	out, err := config.FFprobeCommandContext(probeCtx,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "frame=pkt_pts_time",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-read_intervals", "%+#1",
		videoPath,
	).Output()
	if err != nil {
		return 0
	}
	pts, parseErr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if parseErr != nil {
		return 0
	}
	if pts <= 0 {
		return 0
	}
	return pts
}
