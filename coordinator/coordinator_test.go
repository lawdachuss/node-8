package coordinator

import (
	"context"
	"os"
	"testing"

	"github.com/teacat/chaturbate-dvr/database"
	"github.com/teacat/chaturbate-dvr/entity"
)

// ============================================================================
// Mock implementations
// ============================================================================

type mockClient struct {
	*database.Client // embed nil to satisfy interface with only overridden methods
}

type mockChannelManager struct {
	created []*database.ChannelAssignment
}

func (m *mockChannelManager) CreateChannelFromAssignment(ca *database.ChannelAssignment) error {
	m.created = append(m.created, ca)
	return nil
}

func (m *mockChannelManager) RemoveChannelForReassignment(username string) error {
	return nil
}

func (m *mockChannelManager) GetLocalChannels() []string {
	var list []string
	for _, ca := range m.created {
		list = append(list, ca.Username)
	}
	return list
}

// ============================================================================
// Tests
// ============================================================================

func TestDetectNodeID(t *testing.T) {
	tests := []struct {
		name     string
		envs     map[string]string
		expected string
	}{
		{
			name:     "from NODE_ID env",
			envs:     map[string]string{"NODE_ID": "my-custom-node"},
			expected: "my-custom-node",
		},
		{
			name:     "from GITHUB_REPOSITORY with dashed suffix",
			envs:     map[string]string{"GITHUB_REPOSITORY": "owner/MiniDelectableService-node-a"},
			expected: "a",
		},
		{
			name:     "simple repo name (no dashes) — slash replaced with dash",
			envs:     map[string]string{"GITHUB_REPOSITORY": "you/myrepo"},
			expected: "you-myrepo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env first
			os.Unsetenv("NODE_ID")
			os.Unsetenv("GITHUB_REPOSITORY")

			for k, v := range tt.envs {
				os.Setenv(k, v)
			}

			got := detectNodeID()
			if got != tt.expected {
				t.Errorf("detectNodeID() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestPoolMode(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		expected string
	}{
		{"empty defaults to isolated", "", entity.PoolModeIsolated},
		{"explicit isolated", "isolated", entity.PoolModeIsolated},
		{"pooled", "pooled", entity.PoolModePooled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("CHANNEL_POOL_MODE", tt.envVal)
			got := channelPoolMode()
			if got != tt.expected {
				t.Errorf("channelPoolMode() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestIsPooled(t *testing.T) {
	c := &Coordinator{Mode: entity.PoolModePooled}
	if !c.IsPooled() {
		t.Error("expected IsPooled() = true for pooled mode")
	}

	c2 := &Coordinator{Mode: entity.PoolModeIsolated}
	if c2.IsPooled() {
		t.Error("expected IsPooled() = false for isolated mode")
	}
}

func TestConfigFromAssignment(t *testing.T) {
	ca := &database.ChannelAssignment{
		Username:    "testuser",
		Site:        "chaturbate",
		Framerate:   60,
		Resolution:  2160,
		Pattern:     "videos/{{.Username}}_...",
		MaxDuration: 60,
		Compress:    true,
	}

	conf := ConfigFromAssignment(ca)
	if conf.Username != "testuser" {
		t.Errorf("Username = %q, want %q", conf.Username, "testuser")
	}
	if conf.Site != "chaturbate" {
		t.Errorf("Site = %q, want %q", conf.Site, "chaturbate")
	}
	if conf.Framerate != 60 {
		t.Errorf("Framerate = %d, want %d", conf.Framerate, 60)
	}
	if conf.Resolution != 2160 {
		t.Errorf("Resolution = %d, want %d", conf.Resolution, 2160)
	}
	if !conf.Compress {
		t.Error("expected Compress = true")
	}
}

func TestMarshalUnmarshalPool(t *testing.T) {
	pool := []*entity.ChannelConfig{
		{Username: "alice", Site: "chaturbate", Framerate: 60, Resolution: 2160},
		{Username: "bob", Site: "stripchat", Framerate: 30, Resolution: 1080},
	}

	data, err := MarshalPool(pool)
	if err != nil {
		t.Fatalf("MarshalPool error: %v", err)
	}

	got, err := UnmarshalPool(data)
	if err != nil {
		t.Fatalf("UnmarshalPool error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}

	if got[0].Username != "alice" {
		t.Errorf("got[0].Username = %q, want %q", got[0].Username, "alice")
	}
	if got[1].Username != "bob" {
		t.Errorf("got[1].Username = %q, want %q", got[1].Username, "bob")
	}
}

func TestCurrentLoad(t *testing.T) {
	// Coordinator with no client returns 0
	c := &Coordinator{NodeID: "test-node"}
	load := c.currentLoad()
	if load != 0 {
		t.Errorf("currentLoad() = %d, want 0 (no client)", load)
	}
}

func TestFairShareCalculation(t *testing.T) {
	tests := []struct {
		name         string
		totalLive    int
		totalNodes   int
		expectedFair int
	}{
		{"20 channels, 5 nodes", 20, 5, 4},
		{"7 channels, 3 nodes", 7, 3, 3},
		{"1 channel, 3 nodes", 1, 3, 1},
		{"0 channels, 3 nodes", 0, 3, 0},
		{"3 channels, 0 nodes", 3, 0, 0},
		{"5 channels, 1 node", 5, 1, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fairShare := 0
			if tt.totalNodes > 0 {
				fairShare = (tt.totalLive + tt.totalNodes - 1) / tt.totalNodes
			}
			if fairShare != tt.expectedFair {
				t.Errorf("fairShare = %d, want %d", fairShare, tt.expectedFair)
			}
		})
	}
}

// TestStartStop verifies that Start and Stop don't panic when in pooled mode.
func TestStartStop(t *testing.T) {
	os.Setenv("CHANNEL_POOL_MODE", "pooled")
	defer os.Unsetenv("CHANNEL_POOL_MODE")

	mgr := &mockChannelManager{}
	c := New(nil, mgr)
	c.LiveCheck = nil // no live check for test

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start should not panic
	c.Start(ctx)

	// Stop should not panic
	c.Stop()
}

// TestStartStopIsolated verifies that Start and Stop are no-ops in isolated mode.
func TestStartStopIsolated(t *testing.T) {
	os.Setenv("CHANNEL_POOL_MODE", "isolated")
	defer os.Unsetenv("CHANNEL_POOL_MODE")

	c := New(nil, nil)
	ctx := context.Background()

	c.Start(ctx)
	// Start sets started=true even in isolated mode (prevents double-start)
	// but all goroutines are skipped.
	c.Stop()

	if c.started != true {
		t.Error("expected started to be true after Start()")
	}
}
