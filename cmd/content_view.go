package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// contentLayout uses display cells, not encoded byte lengths, for wrapping.
func (m model) contentLayout() (width int, lines []string, visible int) {
	body := pageContent(m.page)
	width = max(72, lipgloss.Width(body))
	if m.width > 0 {
		width = min(width, max(1, m.width-4))
	}
	lines = strings.Split(ansi.Wrap(body, width, ""), "\n")
	visible = len(lines)
	if m.height > 0 {
		overhead := 11
		if m.height < 14 {
			overhead = 4
		}
		visible = max(1, m.height-overhead)
	}
	return width, lines, visible
}

func (m model) maxContentScroll() int {
	if m.page < 0 {
		return 0
	}
	_, lines, visible := m.contentLayout()
	return max(0, len(lines)-visible)
}

func contentView(m model) tea.View {
	width, lines, visible := m.contentLayout()
	offset := max(0, min(m.scroll, max(0, len(lines)-visible)))
	body := strings.Join(lines[offset:min(len(lines), offset+visible)], "\n")
	rule := lipgloss.NewStyle().Foreground(lipgloss.Color("#626058")).Render(strings.Repeat("─", width))
	title := accent.Italic(true).Render("OWEN HYNES")
	heading := accent.Italic(true).Render(fmt.Sprintf("%02d / %s", m.page+1, pages[m.page]))
	help := "esc back · q quit"
	if len(lines) > visible {
		help = "↑↓/jk scroll · esc back · q quit"
	}
	frame := []string{title, dim.Render("owenhynes.com"), "", rule, "", heading, "", body, "", rule, "", dim.Render(help)}
	if m.height > 0 && m.height < 14 {
		frame = []string{title, heading, body, rule, dim.Render(help)}
	}
	rendered := strings.Split(strings.Join(frame, "\n"), "\n")
	if m.height > 0 && len(rendered) > m.height {
		rendered = rendered[:m.height]
	}
	for i, line := range rendered {
		rendered[i] = ansi.Truncate(line, width, "")
	}
	content := strings.Join(rendered, "\n")
	if m.width > 0 && m.height > 0 {
		content = lipgloss.NewStyle().Width(lipgloss.Width(content)).Render(content)
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.BackgroundColor = lipgloss.Color("#1e1e1c")
	view.ForegroundColor = lipgloss.Color("#e5dece")
	return view
}
