package main

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestKeywordsUseBoldItalicEditorialStyle(t *testing.T) {
	if !accent.GetBold() || !accent.GetItalic() {
		t.Fatal("keywords must use bold italic emphasis")
	}
	if dim.GetBold() || dim.GetItalic() {
		t.Fatal("supporting text must stay understated")
	}
}

func TestContentPagesUseEditorialTheme(t *testing.T) {
	for page, name := range pages {
		t.Run(name, func(t *testing.T) {
			m := newModel()
			m.page = page
			view := m.View()
			plain := ansi.Strip(view.Content)
			for _, text := range []string{"OWEN HYNES", "owenhynes.com", fmt.Sprintf("%02d / %s", page+1, name), "esc back", "q quit"} {
				if !strings.Contains(plain, text) {
					t.Errorf("page missing %q", text)
				}
			}
			if strings.ContainsAny(plain, "╔╗╚╝║═") {
				t.Error("old boxed heading still present")
			}
			if !view.AltScreen {
				t.Error("alternate screen disabled")
			}
			if view.BackgroundColor == nil || view.ForegroundColor == nil {
				t.Fatal("missing explicit screen colours")
			}
			if got := color.NRGBAModel.Convert(view.BackgroundColor); got != (color.NRGBA{R: 30, G: 30, B: 28, A: 255}) {
				t.Errorf("background: %v", got)
			}
			if got := color.NRGBAModel.Convert(view.ForegroundColor); got != (color.NRGBA{R: 229, G: 222, B: 206, A: 255}) {
				t.Errorf("foreground: %v", got)
			}
			// Styling must not remove or rewrite existing page information.
			if !strings.Contains(plain, ansi.Strip(pageContent(page))) {
				t.Error("page content was changed or omitted")
			}
		})
	}
}

func TestContactDetailsAreOnSeparateLines(t *testing.T) {
	lines := strings.Split(ansi.Strip(pageContent(4)), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "github.com/meanybeany420") || !strings.Contains(lines[1], "owenh682") {
		t.Fatal("contact details run together instead of using separate rows")
	}
}

func TestContentPagesFitSmallTerminal(t *testing.T) {
	for page := range pages {
		m := model{page: page, width: 40, height: 18}
		view := m.View()
		if lipgloss.Width(view.Content) > 40 || lipgloss.Height(view.Content) > 18 {
			t.Errorf("page %d exceeds 40x18", page)
		}
		if !strings.Contains(ansi.Strip(view.Content), "esc back") {
			t.Errorf("page %d loses back control", page)
		}
	}
}

func TestContentPageScrollAndReturn(t *testing.T) {
	m := model{page: 1, width: 40, height: 18}
	first := m.View().Content
	m = updateKey(t, m, key(tea.KeyEnd))
	last := m.View().Content
	if first == last {
		t.Fatal("End does not scroll the long project page")
	}
	if !strings.Contains(ansi.Strip(last), "configuration utility.") {
		t.Fatal("last project lines are not reachable")
	}
	m = updateKey(t, m, key(tea.KeyHome))
	if m.View().Content != first {
		t.Fatal("Home does not return to the first page lines")
	}
	m = updateKey(t, m, key('j'))
	if m.View().Content == first {
		t.Fatal("j does not scroll")
	}
	m = updateKey(t, m, key('k'))
	if m.View().Content != first {
		t.Fatal("k does not restore the initial position")
	}
	m = updateKey(t, m, key(tea.KeyEnd))
	m = updateKey(t, m, key(tea.KeyEscape))
	m.cursor = 1
	m = updateKey(t, m, key(tea.KeyEnter))
	if m.View().Content != first {
		t.Fatal("reopening a page retains its old scroll position")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(model)
	if !strings.Contains(strings.Join(strings.Fields(ansi.Strip(m.View().Content)), " "), strings.Join(strings.Fields(ansi.Strip(pageContent(1))), " ")) {
		t.Fatal("resize did not restore the complete project page")
	}
}

func TestContentPagesFitStandardTerminal(t *testing.T) {
	for page := range pages {
		m := model{page: page, width: 120, height: 40}
		v := m.View()
		if lipgloss.Width(v.Content) > 120 || lipgloss.Height(v.Content) > 40 {
			t.Errorf("page %d exceeds terminal", page)
		}
		next, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
		if got := next.(model).View(); lipgloss.Width(got.Content) > 160 || lipgloss.Height(got.Content) > 50 {
			t.Errorf("page %d exceeds resized terminal", page)
		}
	}
}
