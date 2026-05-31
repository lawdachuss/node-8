package manager

import (
	"sort"
	"testing"

	"github.com/teacat/chaturbate-dvr/entity"
)

func TestChannelSortPriorityOrdersRecordingPausedOffline(t *testing.T) {
	channels := []*entity.ChannelInfo{
		{Username: "offline"},
		{Username: "paused_offline", IsPaused: true},
		{Username: "recording", IsOnline: true},
		{Username: "reconnecting", IsConnecting: true},
		{Username: "paused_live", IsPaused: true, IsOnline: true},
	}

	sort.Slice(channels, func(i, j int) bool {
		pi, pj := channelSortPriority(channels[i]), channelSortPriority(channels[j])
		if pi != pj {
			return pi < pj
		}
		return channels[i].Username < channels[j].Username
	})

	got := make([]string, len(channels))
	for i, ch := range channels {
		got[i] = ch.Username
	}
	want := []string{"recording", "paused_live", "paused_offline", "reconnecting", "offline"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sort order = %v, want %v", got, want)
		}
	}
}
