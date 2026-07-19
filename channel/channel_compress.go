package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/teacat/chaturbate-dvr/config"
	"github.com/teacat/chaturbate-dvr/internal"
)

// Track IDs for muxed output
const (
	videoTrackID uint32 = 1
	audioTrackID uint32 = 2
)

// GPU encoder detection cache
var (
	detectedEncoder     videoEncoder
	detectedEncoderOnce sync.Once
)

// videoEncoder represents a video encoder configuration
type videoEncoder struct {
	name  string   // display name
	codec string   // ffmpeg codec name
	args  []string // additional encoder arguments
}

// availableEncoders lists GPU encoders in priority order, with CPU fallback last
var availableEncoders = []videoEncoder{
	// NVIDIA NVENC (CQ 0-51, lower = better quality; 23 ≈ visually lossless)
	{"NVENC", "h264_nvenc", []string{"-preset", "p4", "-rc", "vbr", "-cq", "23", "-b:v", "0"}},
	// AMD AMF
	{"AMF", "h264_amf", []string{"-quality", "balanced", "-rc", "vbr_latency", "-qp_i", "22", "-qp_p", "22"}},
	// Intel Quick Sync
	{"QSV", "h264_qsv", []string{"-preset", "medium", "-global_quality", "22"}},
	// macOS VideoToolbox
	{"VideoToolbox", "h264_videotoolbox", []string{"-q:v", "55"}},
	// CPU fallback (veryfast+CRF 20: ~2x faster than medium, still much better quality than raw TS)
	{"CPU", "libx264", []string{"-preset", "veryfast", "-crf", "20"}},
}

// detectEncoder finds the best available encoder
func detectEncoder() (videoEncoder, string) {
	defer config.AcquireFFmpegHeavy()()
	for _, enc := range availableEncoders {
		// Test if encoder is available by running ffmpeg with it
		cmd := config.FFmpegCommand("-hide_banner", "-f", "lavfi", "-i", "nullsrc=s=256x256:d=1", "-c:v", enc.codec, "-f", "null", "-")
		if err := cmd.Run(); err == nil {
			return enc, enc.name
		}
	}
	// Should not reach here since libx264 is always available if ffmpeg is installed
	return availableEncoders[len(availableEncoders)-1], "CPU"
}

// getEncoder returns the cached encoder or detects one
func getEncoder() videoEncoder {
	detectedEncoderOnce.Do(func() {
		enc, _ := detectEncoder()
		detectedEncoder = enc
	})
	return detectedEncoder
}

// CompressFile compresses a video file (.ts or .mp4) to .mkv format using ffmpeg in the background.
// Uses hardware GPU encoding if available, falls back to CPU (libx264).
// After successful compression, the original file is deleted.
func (ch *Channel) CompressFile(srcPath string) {
	ch.UploadWg.Add(1)
	go func() {
		defer ch.UploadWg.Done()

		// Track active compression jobs so the UI can show the indicator
		atomic.AddInt32(&ch.CompressingCount, 1)
		go ch.Update()
		defer func() {
			atomic.AddInt32(&ch.CompressingCount, -1)
			go ch.Update()
		}()

		ext := filepath.Ext(srcPath)
		mkvPath := strings.TrimSuffix(srcPath, ext) + ".mkv"
		srcFilename := filepath.Base(srcPath)
		mkvFilename := filepath.Base(mkvPath)

		// Get original file size
		srcInfo, err := os.Stat(srcPath)
		if err != nil {
			ch.Error("compress: failed to stat file: %s", err.Error())
			return
		}
		srcSize := srcInfo.Size()

		// Get the best available encoder
		encoder := getEncoder()

		ch.Info("compress: encoding %s (%s) using %s encoder", srcFilename, internal.FormatFilesize(int(srcSize)), encoder.name)

		// Build ffmpeg command.
		// -af aresample=async=1:first_pts=0 dynamically resamples audio to match video
		// clock, fixing gradual A/V drift that can creep in during long
		// recordings.
		args := []string{"-y", "-i", srcPath, "-c:v", encoder.codec}
		args = append(args, encoder.args...)
		args = append(args, "-c:a", "aac", "-b:a", "128k",
			"-af", "aresample=async=1:first_pts=0",
			mkvPath)

		defer config.AcquireFFmpegHeavy()()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		cmd := config.FFmpegCommandContext(ctx, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			ch.Error("compress: failed %s - %s", srcFilename, err.Error())
			if len(output) > 0 {
				outStr := string(output)
				if len(outStr) > 500 {
					outStr = outStr[len(outStr)-500:]
				}
				ch.Error("compress: ffmpeg: %s", outStr)
			}
			ch.Info("compress: compression failed — moving uncompressed %s into pipeline instead of abandoning it", srcFilename)
			ch.MoveToOutputDir(srcPath)
			return
		}

		// Get compressed file size
		mkvInfo, err := os.Stat(mkvPath)
		if err != nil {
			ch.Error("compress: failed to stat mkv: %s", err.Error())
			os.Remove(mkvPath) // clean up incomplete output file
			return
		}
		mkvSize := mkvInfo.Size()

		// Calculate compression ratio
		ratio := float64(mkvSize) / float64(srcSize) * 100

		// Delete the original file after successful compression
		if err := os.Remove(srcPath); err != nil {
			ch.Error("compress: failed to delete %s - %s (continuing)", srcFilename, err.Error())
		} else {
			ch.Info("delete: removed original %s after compression", srcFilename)
		}

		ch.Info("compress: done %s -> %s (%s, %.1f%%)", srcFilename, mkvFilename, internal.FormatFilesize(int(mkvSize)), ratio)

		ch.MoveToOutputDir(mkvPath)
	}()
}

// probeFMP4StartTime probes a single-track fragmented MP4 file for its first
// packet's presentation timestamp (PTS).  Returns -1 if the stream type is not
// found or probing fails.
func probeFMP4StartTime(path, streamType string) float64 {
	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, perr := config.FFprobeCommandContext(probeCtx,
		"-v", "error",
		"-select_streams", streamType+":0",
		"-show_entries", "stream=start_time",
		"-of", "json",
		path,
	).Output()
	if perr != nil {
		return -1
	}
	var p struct {
		Streams []struct {
			StartTime string `json:"start_time"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return -1
	}
	if len(p.Streams) == 0 {
		return -1
	}
	v, e := strconv.ParseFloat(p.Streams[0].StartTime, 64)
	if e != nil {
		return -1
	}
	return v
}

// MuxAV combines separate video and audio source files into a single MP4 container.
//
// The function first probes both inputs for their first-packet presentation
// timestamps.  When the audio starts after the video (very common on the first
// poll of a live LL-HLS stream where separate audio/video playlists start at
// different wall-clock moments), an -itsoffset is applied to the audio input so
// both tracks begin at the same time.  This one-pass approach eliminates the
// need for a second realignment pass (the old alignAVStart post-process) and
// avoids the desync where the sound from the later audio segment ends up
// playing against the earlier video content, so users hear audio running
// seconds ahead of video.
func (ch *Channel) MuxAV(videoPath, audioPath, outputPath string) error {
	// Probe both inputs for their starting PTS so we can align them.
	videoStart := probeFMP4StartTime(videoPath, "v")
	audioStart := probeFMP4StartTime(audioPath, "a")

	// Calculate the shift needed to align audio to video.
	// shift = videoStart - audioStart: positive means audio is behind,
	// negative means audio is ahead.  We only correct offsets larger
	// than 50ms to avoid unnecessary processing.
	var shift float64
	if videoStart >= 0 && audioStart >= 0 {
		shift = videoStart - audioStart
		if shift < 0.050 && shift > -0.050 {
			shift = 0 // already aligned within perception threshold
		} else {
			ch.Info("mux: detected A/V offset of %.3fs (video:%.3fs audio:%.3fs) — applying -itsoffset %.3f to align",
				audioStart-videoStart, videoStart, audioStart, shift)
		}
	} else if videoStart < 0 || audioStart < 0 {
		ch.Info("mux: could not probe start times (v:%.3f a:%.3f) — muxing without offset correction",
			videoStart, audioStart)
	}

	// Build the ffmpeg command with probe-based alignment:
	//   - -copyts preserves the (adjusted) timestamps so content alignment is exact
	//   - -itsoffset on audio shifts it to match video's start time
	//   - -shortest prevents a stray partial segment from extending the output
	//   - -avoid_negative_ts make_zero handles B-frame reordering (negative DTS)
	//   - -fflags +genpts ensures every packet has a valid PTS
	args := []string{
		"-y",
		"-fflags", "+genpts",
		"-i", videoPath,
	}
	if shift != 0 {
		args = append(args,
			"-itsoffset", fmt.Sprintf("%f", shift),
		)
	}
	args = append(args,
		"-i", audioPath,
		"-map", "0:v?",
		"-map", "1:a?",
		"-c", "copy",
		"-copyts",
		"-shortest",
		"-avoid_negative_ts", "make_zero",
		"-movflags", "+faststart",
		outputPath,
	)

	defer config.AcquireFFmpeg()()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := config.FFmpegCommandContext(ctx, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			outStr := string(output)
			if len(outStr) > 500 {
				outStr = outStr[len(outStr)-500:]
			}
			ch.Error("mux: ffmpeg: %s", outStr)
		}
		return fmt.Errorf("mux audio/video: %w", err)
	}

	if shift != 0 {
		ch.Info("mux: combined %s + %s -> %s (synced, offset=%.3fs)",
			filepath.Base(videoPath), filepath.Base(audioPath), filepath.Base(outputPath), shift)
	} else {
		ch.Info("mux: combined %s + %s -> %s",
			filepath.Base(videoPath), filepath.Base(audioPath), filepath.Base(outputPath))
	}
	return nil
}

// MuxAVNative combines separate fragmented MP4 audio/video tracks without ffmpeg.
// Unlike MuxAV (which uses ffmpeg with -itsoffset correction), this path aligns
// audio and video by probing their TFDT (Track Fragment Decode Time) boxes and
// shifting audio fragment timestamps by the offset.  Fragment counts are also
// trimmed to the shorter track to prevent one side from extending past the other.
func (ch *Channel) MuxAVNative(videoPath, audioPath, outputPath string) error {
	// Probe TFDT-based start times for native alignment.
	videoStart := probeFMP4StartTime(videoPath, "v")
	audioStart := probeFMP4StartTime(audioPath, "a")
	offsetSeconds := 0.0
	if videoStart >= 0 && audioStart >= 0 && audioStart != videoStart {
		offsetSeconds = audioStart - videoStart
		if offsetSeconds < 0.050 && offsetSeconds > -0.050 {
			offsetSeconds = 0 // within perception threshold
		} else if offsetSeconds != 0 {
			ch.Info("mux: native: detected A/V offset of %.3fs — will offset audio fragments", offsetSeconds)
		}
	}

	videoFile, err := mp4.ReadMP4File(videoPath)
	if err != nil {
		return fmt.Errorf("decode video mp4: %w", err)
	}
	audioFile, err := mp4.ReadMP4File(audioPath)
	if err != nil {
		return fmt.Errorf("decode audio mp4: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create mux output: %w", err)
	}

	warn := func(msg string) { ch.Info("mux: %s", msg) }
	if err := writeCombinedFragmentedMP4(outFile, videoFile, audioFile, offsetSeconds, warn); err != nil {
		outFile.Close()
		if rmErr := os.Remove(outputPath); rmErr != nil {
			ch.Warn("mux: failed to remove incomplete output %s: %v", outputPath, rmErr)
		}
		return fmt.Errorf("native mux audio/video: %w", err)
	}
	outFile.Close()

	if offsetSeconds != 0 {
		ch.Info("mux: combined %s + %s -> %s (native, synced offset=%.3fs)",
			filepath.Base(videoPath), filepath.Base(audioPath), filepath.Base(outputPath), offsetSeconds)
	} else {
		ch.Info("mux: combined %s + %s -> %s (native)",
			filepath.Base(videoPath), filepath.Base(audioPath), filepath.Base(outputPath))
	}
	return nil
}

func writeCombinedFragmentedMP4(w io.Writer, videoFile, audioFile *mp4.File, audioOffsetSeconds float64, warn func(string)) error {
	_, videoTrex, err := sourceTrack(videoFile, "vide")
	if err != nil {
		return fmt.Errorf("load video track: %w", err)
	}
	audioTrack, audioTrex, err := sourceTrack(audioFile, "soun")
	if err != nil {
		return fmt.Errorf("load audio track: %w", err)
	}

	// Combine fragments BEFORE reassigning track IDs — GetFullSamples
	// matches source traf boxes by trex.TrackID, which must still hold
	// the original value from the source file.
	videoFragments := collectFragments(videoFile)
	audioFragments := collectFragments(audioFile)

	// Synchronize fragment counts by trimming to the shorter track.
	// This prevents A/V drift caused by timing differences during live
	// LL-HLS polling where one playlist may have a few extra segments.
	originalVideoCount := len(videoFragments)
	originalAudioCount := len(audioFragments)
	if originalVideoCount != originalAudioCount {
		minCount := originalVideoCount
		if originalAudioCount < minCount {
			minCount = originalAudioCount
		}
		if warn != nil {
			warn(fmt.Sprintf("fragment count mismatch (video=%d, audio=%d); trimming to %d fragments for perfect sync", originalVideoCount, originalAudioCount, minCount))
		}
		videoFragments = videoFragments[:minCount]
		audioFragments = audioFragments[:minCount]
	}

	segments, err := combineTrackFragments(videoFragments, videoTrex, audioFragments, audioTrex, audioOffsetSeconds)
	if err != nil {
		return err
	}

	ftyp := videoFile.Init.Ftyp
	moov := videoFile.Init.Moov
	if len(moov.Traks) != 1 || moov.Mvex == nil || len(moov.Mvex.Trexs) != 1 {
		return fmt.Errorf("expected single-track video init")
	}

	moov.Traks[0].Tkhd.TrackID = videoTrackID
	moov.Mvex.Trexs[0].TrackID = videoTrackID

	audioTrack.Tkhd.TrackID = audioTrackID
	audioTrex.TrackID = audioTrackID

	moov.AddChild(audioTrack)
	moov.Mvex.AddChild(audioTrex)
	moov.Mvhd.NextTrackID = audioTrackID + 1

	out := mp4.NewFile()
	out.AddChild(ftyp, 0)
	out.AddChild(moov, ftyp.Size())
	for _, segment := range segments {
		out.AddMediaSegment(segment)
	}

	return out.Encode(w)
}

func sourceTrack(file *mp4.File, handlerType string) (*mp4.TrakBox, *mp4.TrexBox, error) {
	if file == nil || file.Init == nil || file.Init.Moov == nil {
		return nil, nil, fmt.Errorf("missing init segment")
	}
	if len(file.Init.Moov.Traks) != 1 {
		return nil, nil, fmt.Errorf("expected exactly one track, got %d", len(file.Init.Moov.Traks))
	}

	trak := file.Init.Moov.Traks[0]
	if trak == nil || trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Hdlr == nil {
		return nil, nil, fmt.Errorf("invalid track metadata")
	}
	if trak.Mdia.Hdlr.HandlerType != handlerType {
		return nil, nil, fmt.Errorf("expected %s track, got %s", handlerType, trak.Mdia.Hdlr.HandlerType)
	}
	if file.Init.Moov.Mvex == nil {
		return nil, nil, fmt.Errorf("missing mvex")
	}

	trex, ok := file.Init.Moov.Mvex.GetTrex(trak.Tkhd.TrackID)
	if !ok || trex == nil {
		return nil, nil, fmt.Errorf("missing trex for track %d", trak.Tkhd.TrackID)
	}

	return trak, trex, nil
}

func combineTrackFragments(videoFragments []*mp4.Fragment, videoTrex *mp4.TrexBox, audioFragments []*mp4.Fragment, audioTrex *mp4.TrexBox, _ float64) ([]*mp4.MediaSegment, error) {
	maxFragments := len(videoFragments)
	if len(audioFragments) > maxFragments {
		maxFragments = len(audioFragments)
	}
	if maxFragments == 0 {
		return nil, fmt.Errorf("missing media fragments")
	}

	segments := make([]*mp4.MediaSegment, 0, maxFragments)
	for i := 0; i < maxFragments; i++ {
		trackIDs := make([]uint32, 0, 2)
		if i < len(videoFragments) {
			trackIDs = append(trackIDs, videoTrackID)
		}
		if i < len(audioFragments) {
			trackIDs = append(trackIDs, audioTrackID)
		}

		fragment, err := mp4.CreateMultiTrackFragment(uint32(i+1), trackIDs)
		if err != nil {
			return nil, fmt.Errorf("create fragment %d: %w", i, err)
		}

		if i < len(videoFragments) {
			if err := appendFragmentSamples(fragment, videoFragments[i], videoTrex, videoTrackID); err != nil {
				return nil, fmt.Errorf("append video fragment %d: %w", i, err)
			}
		}
		if i < len(audioFragments) {
			if err := appendFragmentSamples(fragment, audioFragments[i], audioTrex, audioTrackID); err != nil {
				return nil, fmt.Errorf("append audio fragment %d: %w", i, err)
			}
		}

		segment := mp4.NewMediaSegmentWithoutStyp()
		segment.AddFragment(fragment)
		segments = append(segments, segment)
	}

	return segments, nil
}

func appendFragmentSamples(dst, src *mp4.Fragment, trex *mp4.TrexBox, trackID uint32) error {
	fullSamples, err := src.GetFullSamples(trex)
	if err != nil {
		return err
	}
	for _, sample := range fullSamples {
		if err := dst.AddFullSampleToTrack(sample, trackID); err != nil {
			return err
		}
	}
	return nil
}

func collectFragments(file *mp4.File) []*mp4.Fragment {
	var fragments []*mp4.Fragment
	for _, segment := range file.Segments {
		fragments = append(fragments, segment.Fragments...)
	}
	return fragments
}
