package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestNavigationIsTextOnlyAtEverySize(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {32, 18}, {60, 24}, {120, 40}, {160, 50}} {
		m := model{page: -1, width: size[0], height: size[1]}
		view := m.View()
		for _, r := range ansi.Strip(view.Content) {
			if r >= 0x2800 && r <= 0x28ff {
				t.Fatalf("navigation still contains illustration pixels at %dx%d", size[0], size[1])
			}
		}
		for _, name := range pages {
			if !strings.Contains(ansi.Strip(view.Content), name) {
				t.Errorf("missing section %q", name)
			}
		}
		if size[0] == 0 && lipgloss.Width(view.Content) != 60 {
			t.Errorf("menu did not return to its compact 60-column layout: %d", lipgloss.Width(view.Content))
		}
	}
}
