package channel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/server"
)

// TestSegmentsForChannelExcludesOtherChannels is a regression test for the bug
// where recordings from two or more different channels were merged into a single
// output file.  A segment whose filename clearly belongs to channel B must
// never appear in the merge set for channel A — it must be relocated to B's
// (or the _unknown) pending bucket instead.
func TestSegmentsForChannelExcludesOtherChannels(t *testing.T) {
	dir := t.TempDir()
	// pendingSegmentsDir derives the .pending location from server.Config.OutputDir.
	if server.Config == nil {
		server.Config = &entity.Config{}
	}
	orig := server.Config.OutputDir
	server.Config.OutputDir = dir
	t.Cleanup(func() { server.Config.OutputDir = orig })

	// Create pending directories for two distinct channels.
	dirA := filepath.Join(dir, ".pending", "alice")
	dirB := filepath.Join(dir, ".pending", "bob")
	if err := os.MkdirAll(dirA, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0777); err != nil {
		t.Fatal(err)
	}

	// Alice's two real segments + one corrupt/unknown file.
	mustWrite(t, filepath.Join(dirA, "alice_2025-01-01_12-00-00.mp4"), 1024)
	mustWrite(t, filepath.Join(dirA, "alice_2025-01-01_12-30-00.mp4"), 1024)
	// A stray from Bob that was misrouted into Alice's directory.
	mustWrite(t, filepath.Join(dirA, "bob_2025-01-01_13-00-00.mp4"), 1024)
	// A corrupt/truncated file whose owner cannot be determined.
	mustWrite(t, filepath.Join(dirA, "partial-download.tmp"), 1024)

	segs := segmentsForChannel("alice")

	// Only Alice's two segments may remain; Bob's stray and the unknown file
	// must have been relocated out.
	if len(segs) != 2 {
		t.Fatalf("expected 2 segments for alice, got %d: %v", len(segs), segs)
	}
	for _, s := range segs {
		if extractUsernameFromFilename(filepath.Base(s)) != "alice" {
			t.Fatalf("non-alice segment leaked into alice's merge set: %s", filepath.Base(s))
		}
	}

	// Bob's stray should now live in Bob's pending dir.
	if _, err := os.Stat(filepath.Join(dirB, "bob_2025-01-01_13-00-00.mp4")); err != nil {
		t.Errorf("bob's stray segment was not relocated to bob's pending dir: %v", err)
	}
	// The unknown file should be isolated in _unknown, not left in alice's dir.
	if _, err := os.Stat(filepath.Join(dirA, "partial-download.tmp")); err == nil {
		t.Errorf("unknown/corrupt segment was wrongly kept in alice's pending dir")
	}
	if _, err := os.Stat(filepath.Join(dir, ".pending", "_unknown", "partial-download.tmp")); err != nil {
		t.Errorf("unknown/corrupt segment was not isolated to _unknown bucket: %v", err)
	}
}

func mustWrite(t *testing.T, path string, size int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(int64(size)); err != nil {
		t.Fatal(err)
	}
}
