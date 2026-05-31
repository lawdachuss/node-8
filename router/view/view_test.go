package view

import (
	"strings"
	"testing"
)

func TestChannelListItemsUseKeyboardAccessibleButtons(t *testing.T) {
	content, err := FS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	html := string(content)
	if !strings.Contains(html, `<button type="button"`) ||
		!strings.Contains(html, `channel-item`) {
		t.Fatal("channel list items should be rendered as native buttons with class channel-item")
	}
}

func TestSidebarHasPausedGroupingAndIndicator(t *testing.T) {
	content, err := FS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	html := string(content)
	for _, want := range []string{
		`1: 'Paused'`,
		`data-priority="{{ if and .IsOnline (not .IsPaused) }}0{{ else if .IsPaused }}1`,
		`{{ else if .IsPaused }}Paused`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index template missing paused sidebar marker %q", want)
		}
	}
}
