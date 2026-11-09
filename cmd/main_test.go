package main

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"context"
	"errors"
	"flag"
	gossh "golang.org/x/crypto/ssh"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.listen != "127.0.0.1:23234" || cfg.hostKey != ".ssh/id_ed25519" {
		t.Fatalf("defaults: %+v", cfg)
	}
	cfg, err = parseConfig([]string{"-listen", "0.0.0.0:0", "-host-key", "isolated/key"})
	if err != nil || cfg.listen != "0.0.0.0:0" || cfg.hostKey != "isolated/key" {
		t.Fatalf("explicit flags: %+v, %v", cfg, err)
	}
	cfg, err = parseConfig(nil)
	if err != nil || cfg.listen != "127.0.0.1:23234" {
		t.Fatalf("global flag state leaked: %+v %v", cfg, err)
	}
}

func TestParseConfigInvalid(t *testing.T) {
	for _, args := range [][]string{
		{"-wat"}, {"-listen"}, {"-host-key"}, {"extra"},
		{"-listen", ""}, {"-listen", "localhost"}, {"-listen", "localhost:http"},
		{"-listen", "localhost:-1"}, {"-listen", "localhost:65536"}, {"-listen", "::1:22"},
		{"-host-key", ""}, {"-host-key", "   "},
	} {
		if _, err := parseConfig(args); err == nil {
			t.Errorf("accepted invalid args %q", args)
		}
	}
	for _, address := range []string{"localhost:2222", "[::1]:0", ":23234", "0.0.0.0:2222"} {
		if _, err := parseConfig([]string{"-listen", address}); err != nil {
			t.Errorf("valid address %q: %v", address, err)
		}
	}
}

type readinessLog struct{ ready chan string }

func (w readinessLog) Write(p []byte) (int, error) {
	if address, ok := strings.CutPrefix(strings.TrimSpace(string(p)), "SSH portfolio listening on "); ok {
		w.ready <- address
	}
	return len(p), nil
}

func TestServerCancellationClosesListenerAndSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := filepath.Join(t.TempDir(), "hostkey")
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, config{listen: "127.0.0.1:0", hostKey: key}, log.New(readinessLog{ready}, "", 0), 50*time.Millisecond)
	}()
	var address string
	select {
	case address = <-ready:
	case err := <-done:
		t.Fatalf("startup failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	if strings.HasSuffix(address, ":0") {
		t.Fatalf("logged requested rather than bound address: %s", address)
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("isolated host key missing: %v", err)
	}
	client, err := gossh.Dial("tcp", address, &gossh.ClientConfig{User: "test", HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.RequestPty("xterm-256color", 40, 120, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	session.Stdout, session.Stderr = io.Discard, io.Discard
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- session.Wait() }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("normal cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown was not bounded")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("SSH session remained open")
	}
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("listener remained open")
	}
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func updateKey(t *testing.T, m model, msg tea.KeyPressMsg) model {
	t.Helper()
	next, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatal("unexpected command")
	}
	return next.(model)
}

func TestMenuNavigation(t *testing.T) {
	for _, keys := range [][2]rune{{tea.KeyDown, tea.KeyUp}, {'j', 'k'}} {
		t.Run(string(keys[:]), func(t *testing.T) {
			m := newModel()
			if m.cursor != 0 || m.page != -1 || m.Init() != nil {
				t.Fatalf("initial model: %+v", m)
			}
			m = updateKey(t, m, key(keys[1]))
			if m.cursor != 0 {
				t.Fatal("moved above first menu entry")
			}
			for i := 0; i < len(pages)+2; i++ {
				m = updateKey(t, m, key(keys[0]))
			}
			if m.cursor != len(pages)-1 {
				t.Fatalf("lower bound: %+v", m)
			}
			m = updateKey(t, m, key(keys[1]))
			if m.cursor != len(pages)-2 {
				t.Fatalf("up key: %+v", m)
			}
			m = updateKey(t, m, key(tea.KeyEnter))
			if m.page != m.cursor {
				t.Fatalf("enter did not open selected page: %+v", m)
			}
			opened := m
			for _, msg := range []tea.KeyPressMsg{key(keys[0]), key(keys[1]), key(tea.KeyEnter), key('x')} {
				m = updateKey(t, m, msg)
				if m != opened {
					t.Fatalf("page unexpectedly navigated: %+v", m)
				}
			}
			for _, back := range []rune{tea.KeyEscape, tea.KeyBackspace} {
				m = updateKey(t, opened, key(back))
				if m.page != -1 || m.cursor != opened.cursor {
					t.Fatalf("back lost selection: %+v", m)
				}
			}
			for i := 0; i < len(pages)+2; i++ {
				m = updateKey(t, m, key(keys[1]))
			}
			if m.cursor != 0 {
				t.Fatalf("upper bound: %+v", m)
			}
		})
	}
}

func TestQuitKeys(t *testing.T) {
	for _, msg := range []tea.KeyPressMsg{key('q'), {Code: 'c', Mod: tea.ModCtrl}} {
		for _, page := range []int{-1, 0, 4} {
			m := model{cursor: 2, page: page}
			next, cmd := m.Update(msg)
			if next.(model) != m || cmd == nil {
				t.Fatalf("quit %v: model=%+v, cmd=%v", msg, next, cmd)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("%v did not quit", msg)
			}
		}
	}
}

func TestSessionStateIndependence(t *testing.T) {
	first, second := newModel(), newModel()
	first = updateKey(t, first, key('j'))
	first = updateKey(t, first, key(tea.KeyEnter))
	if first.cursor != 1 || first.page != 1 || second.cursor != 0 || second.page != -1 {
		t.Fatalf("state leaked: %+v %+v", first, second)
	}
}

func TestViewCentersOnResize(t *testing.T) {
	m := newModel()
	original := m.View().Content
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 160, Height: 50}} {
		next, cmd := m.Update(size)
		if cmd != nil {
			t.Fatal("resize returned an unexpected command")
		}
		m = next.(model)
		view := m.View()
		lines := strings.Split(view.Content, "\n")
		if len(lines) != size.Height {
			t.Fatalf("resize to %dx%d: got %d rows", size.Width, size.Height, len(lines))
		}
		contentLines := strings.Split(original, "\n")
		contentWidth := lipgloss.Width(original)
		top := (size.Height - len(contentLines)) / 2
		left := (size.Width - contentWidth) / 2
		for i, line := range contentLines {
			if strings.TrimRight(lines[top+i], " ") != strings.TrimRight(strings.Repeat(" ", left)+line, " ") {
				t.Fatalf("content not centred as a left-aligned block at row %d: %q", top+i, lines[top+i])
			}
		}
		if !view.AltScreen {
			t.Fatal("resize disabled alternate screen")
		}
	}
}

func TestViews(t *testing.T) {
	m := newModel()
	view := m.View()
	if !view.AltScreen {
		t.Fatal("alternate screen disabled")
	}
	for _, text := range append([]string{"Owen Hynes Portfolio", "Select a section:", "❯ About", "↑↓/jk navigate · enter select · esc back · q quit"}, pages...) {
		if !strings.Contains(view.Content, text) {
			t.Errorf("menu missing %q", text)
		}
	}
	for page, text := range []string{"University of Chester", "Kennel Reservation Application", "THEA PhD research project", "Python, Go, Java, C++, Bash, Lua, SQL", "github.com/meanybeany420"} {
		m.page = page
		view = m.View()
		if !view.AltScreen || !strings.Contains(view.Content, pages[page]) || !strings.Contains(view.Content, text) {
			t.Errorf("page %d view: %q", page, view.Content)
		}
		if strings.Contains(view.Content, "Select a section:") {
			t.Errorf("page %d still shows menu", page)
		}
	}
	for _, page := range []int{-1, len(pages)} {
		if pageContent(page) != "" {
			t.Errorf("unexpected content for invalid page %d", page)
		}
	}
	if next, cmd := m.Update(struct{}{}); next.(model) != m || cmd != nil {
		t.Fatal("unknown messages changed state")
	}
}

func TestParseConfigHelp(t *testing.T) {
	for _, arg := range []string{"-h", "-help"} {
		if _, err := parseConfig([]string{arg}); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help: %v", err)
		}
	}
}

func TestServerImmediateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	cfg := config{listen: "127.0.0.1:0", hostKey: filepath.Join(t.TempDir(), "hostkey")}
	go func() { done <- runServer(ctx, cfg, log.New(readinessLog{ready}, "", 0), time.Second) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("immediate cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("immediate cancellation hung")
	}
	address := <-ready
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("listener remained open after immediate cancellation")
	}
}

func TestServerStartupErrors(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	dir := t.TempDir()
	badKey := filepath.Join(dir, "invalid-key")
	if err := os.WriteFile(badKey, []byte("not an SSH private key"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  config
		want string
	}{
		{"bind", config{listen: occupied.Addr().String(), hostKey: filepath.Join(dir, "generated-key")}, "listen on"},
		{"host key", config{listen: "127.0.0.1:0", hostKey: badKey}, "create SSH server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			err := runServer(context.Background(), tc.cfg, log.New(&logs, "", 0), time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if strings.Contains(logs.String(), "listening") {
				t.Fatalf("claimed readiness on startup failure: %s", logs.String())
			}
		})
	}
}

func TestContactHandle(t *testing.T) {
	got := pageContent(4)
	if !strings.Contains(got, "github.com/meanybeany420") || strings.Contains(got, "meany_beany420") {
		t.Fatalf("incorrect contact: %q", got)
	}
}
