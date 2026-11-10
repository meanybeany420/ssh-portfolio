package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// navigationView keeps the landing screen's presentation separate from the
// content pages and the SSH session policy.
func navigationView(m model) tea.View {
	width := 60
	if m.width > 0 {
		width = min(width, max(1, m.width-4))
	}
	charcoal := lipgloss.Color("#1e1e1c")
	ivory := lipgloss.Color("#e5dece")
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#b7b1a5"))
	rule := lipgloss.NewStyle().Foreground(lipgloss.Color("#626058")).Render(strings.Repeat("─", width))
	title := lipgloss.NewStyle().Foreground(ivory).Italic(true).Render("OWEN HYNES")
	lines := []string{title, muted.Render("owenhynes.com"), "", rule, "", muted.Render("Select a section:"), ""}
	for i, label := range pages {
		text := fmt.Sprintf(" %02d  %s", i+1, label)
		style := lipgloss.NewStyle().Foreground(ivory).Width(width)
		if i == m.cursor {
			style = style.Background(ivory).Foreground(charcoal)
			text = ansi.Truncate(text, max(1, width-3), "")
			text += strings.Repeat(" ", max(0, width-2-lipgloss.Width(text))) + "↗ "
		}
		lines = append(lines, style.Render(ansi.Truncate(text, width, "")), "")
	}
	lines = append(lines, rule, "", muted.Render("↑↓/jk navigate · enter select · esc back · q quit"))
	// Compact spacing first; only exceptionally small terminals need clipping.
	if m.height > 0 && len(lines) > m.height {
		compact := make([]string, 0, len(lines))
		for _, line := range lines {
			if line != "" {
				compact = append(compact, line)
			}
		}
		lines = compact
		if len(lines) > m.height {
			lines = lines[:m.height]
		}
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	content := strings.Join(lines, "\n")
	if m.width > 0 && m.height > 0 {
		content = lipgloss.NewStyle().Width(lipgloss.Width(content)).Render(content)
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.BackgroundColor = charcoal
	view.ForegroundColor = ivory
	return view
}
