package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestNavigationEditorialAppearance(t *testing.T) {
	m := newModel()
	view := m.View()
	plain := ansi.Strip(view.Content)
	for _, text := range []string{"OWEN HYNES", "Select a section:", "01  About", "02  Projects", "03  Research & Experience", "04  Skills", "05  Contact"} {
		if !strings.Contains(plain, text) {
			t.Errorf("navigation missing %q", text)
		}
	}
	if strings.ContainsAny(plain, "╔╗╚╝║═❯") {
		t.Error("navigation still uses the old boxed terminal styling")
	}
	if view.BackgroundColor == nil {
		t.Error("navigation has no explicit charcoal background")
	}
	if view.ForegroundColor == nil {
		t.Error("navigation has no explicit ivory foreground")
	}
}

func TestNavigationSelectionMovesWithoutChangingLabels(t *testing.T) {
	m := newModel()
	initial := m.View().Content
	m = updateKey(t, m, key('j'))
	moved := m.View().Content
	if initial == moved {
		t.Fatal("selection has no visual feedback")
	}
	for _, label := range pages {
		if !strings.Contains(ansi.Strip(moved), label) {
			t.Errorf("missing label %q", label)
		}
	}
	if strings.Contains(ansi.Strip(initial), "→ Projects") {
		t.Fatal("unselected row marked active")
	}
	if !strings.Contains(ansi.Strip(moved), "02  Projects") {
		t.Fatal("selected Projects label missing")
	}
}

func TestNavigationFitsNarrowTerminal(t *testing.T) {
	m := newModel()
	for _, size := range []tea.WindowSizeMsg{{Width: 32, Height: 18}, {Width: 60, Height: 24}, {Width: 120, Height: 40}, {Width: 160, Height: 50}} {
		next, _ := m.Update(size)
		m = next.(model)
		view := m.View()
		if lipgloss.Height(view.Content) > size.Height || lipgloss.Width(view.Content) > size.Width {
			t.Errorf("navigation exceeds %dx%d: %dx%d", size.Width, size.Height, lipgloss.Width(view.Content), lipgloss.Height(view.Content))
		}
		for _, label := range pages {
			if !strings.Contains(ansi.Strip(view.Content), label) {
				t.Errorf("missing %q at %dx%d", label, size.Width, size.Height)
			}
		}
	}
}
