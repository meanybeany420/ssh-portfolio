package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/activeterm"
	"charm.land/wish/v2/bubbletea"
)

var (
	accent = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#e5dece")).
		Bold(true).
		Italic(true)

	dim = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#b7b1a5"))
)

var pages = []string{
	"About",
	"Projects",
	"Research & Experience",
	"Skills",
	"Contact",
}

type model struct {
	cursor int
	page   int
	width  int
	height int
	scroll int
}

func newModel() model {
	return model{page: -1}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = boundedDimension(msg.Width, maxWidth)
		m.height = boundedDimension(msg.Height, maxHeight)
		m.scroll = min(m.scroll, m.maxContentScroll())

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit

		case "esc", "backspace":
			m.page = -1
			m.scroll = 0

		case "up", "k":
			if m.page == -1 && m.cursor > 0 {
				m.cursor--
			} else if m.page >= 0 {
				m.scroll = max(0, m.scroll-1)
			}

		case "down", "j":
			if m.page == -1 && m.cursor < len(pages)-1 {
				m.cursor++
			} else if m.page >= 0 {
				m.scroll = min(m.maxContentScroll(), m.scroll+1)
			}

		case "pgdown", "pgup":
			if m.page >= 0 {
				_, _, step := m.contentLayout()
				if msg.String() == "pgup" {
					step = -step
				}
				m.scroll = max(0, min(m.maxContentScroll(), m.scroll+step))
			}
		case "home":
			m.scroll = 0
		case "end":
			if m.page >= 0 {
				m.scroll = m.maxContentScroll()
			}
		case "enter":
			if m.page == -1 {
				m.page = m.cursor
				m.scroll = 0
			}
		}
	}

	return m, nil
}

func (m model) View() tea.View {
	if m.page == -1 {
		return navigationView(m)
	}
	return contentView(m)
}

func pageContent(page int) string {
	switch page {
	case 0:
		return "I'm a second-year Computer Science / Cybersecurity student at\n" +
			"the " + accent.Render("University of Chester") + ", currently predicted a " + accent.Render("First Class") + ".\n" +
			"I particularly enjoy " + accent.Render("CTF challenges") + " and " + accent.Render("OSINT") + ", alongside exploring\n" +
			"Linux systems and building tools for my own workflow.\n\n" +
			"Outside computing, I enjoy endurance sports and the outdoors,\n" +
			"especially long-distance running, triathlon, hiking, and mountain\n" +
			"climbing. I'm also an avid reader and write novels on Royal Road\n" +
			"in my spare time."

	case 1:
		return accent.Render("01  SSH Portfolio") + "\n" +
			"    Interactive portfolio served over SSH.\n" +
			"    Go · Wish · Bubble Tea\n\n" +
			accent.Render("02  Neovim Theme") + "\n" +
			"    LSP semantic tokens, diagnostics and Tree-sitter highlights.\n" +
			"    Lua · Neovim\n\n" +
			accent.Render("03  Kennel Reservation Application") + "\n" +
			"    Desktop interface with booking validation and capacity checks.\n" +
			"    Java Swing · SQLite · JDBC\n\n" +
			accent.Render("04  Linux Development Environment (ongoing)") + "\n" +
			"    Version-controlled Arch Linux, Hyprland, tmux and Neovim setup.\n" +
			"    Integrated language tooling and diagnostics.\n" +
			"    Lua screenshot utility with region selection, timestamped\n" +
			"    captures, clipboard integration and desktop notifications.\n" +
			"    Bash display detection and Hyprland configuration utility."

	case 2:
		return accent.Render("Research / Volunteer Contribution") + "\n" +
			"Firmware vulnerability scanning for the THEA PhD research project.\n\n" +
			accent.Render("Experience / FHJ Computer Support") + "\n" +
			"Supported the team with customer enquiries and technical issues."

	case 3:
		return accent.Render("Languages:") + " Python, Go, Java, C++, Bash, Lua, SQL\n" +
			accent.Render("Databases:") + " SQLite\n" +
			accent.Render("Systems:") + " Arch Linux, Ubuntu, Ubuntu Server, Windows\n" +
			accent.Render("Tools:") + " Neovim, tmux, Git, command-line utilities"

	case 4:
		return accent.Render("GitHub:") + " github.com/meanybeany420\n" +
			accent.Render("contact:") + "owenh682 (at) proton (dot) me"

	default:
		return ""
	}
}

type config struct {
	listen  string
	hostKey string
}

func parseConfig(args []string) (config, error) {
	cfg := config{}
	flags := flag.NewFlagSet("ssh-portfolio", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.listen, "listen", "127.0.0.1:23234", "SSH listen address (host:port)")
	flags.StringVar(&cfg.hostKey, "host-key", ".ssh/id_ed25519", "SSH host key path")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected positional arguments: %q", flags.Args())
	}
	_, port, err := net.SplitHostPort(cfg.listen)
	if err != nil {
		return config{}, fmt.Errorf("invalid -listen address %q: %w", cfg.listen, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 || strings.Trim(port, "0123456789") != "" {
		return config{}, fmt.Errorf("invalid -listen port %q: must be a number from 0 to 65535", port)
	}
	if strings.TrimSpace(cfg.hostKey) == "" {
		return config{}, fmt.Errorf("-host-key must not be empty")
	}
	return cfg, nil
}

// readyListener reports readiness only after Serve has registered its listener.
// This keeps immediate cancellation from racing the library's shutdown setup.
type readyListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (l *readyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}

func newPortfolioServer(cfg config) (*ssh.Server, error) {
	return wish.NewServer(
		wish.WithAddress(cfg.listen),
		wish.WithHostKeyPath(cfg.hostKey),
		wish.WithMiddleware(
			bubbletea.MiddlewareWithProgramHandler(func(s ssh.Session) *tea.Program {
				return newPortfolioProgram(newModel(), s)
			}),
			activeterm.Middleware(),
		),
		shellOnly,
	)
}

func runServer(ctx context.Context, cfg config, logger *log.Logger, shutdownTimeout time.Duration) error {
	server, err := newPortfolioServer(cfg)
	if err != nil {
		return fmt.Errorf("create SSH server: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", cfg.listen, err)
	}
	defer listener.Close()
	defer server.Close()
	limited := newLimitedListener(listener)
	defer limited.closeConnections()
	ready := &readyListener{Listener: limited, ready: make(chan struct{})}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ready) }()
	select {
	case <-ready.ready:
		logger.Printf("SSH portfolio listening on %s", listener.Addr())
	case err := <-served:
		return fmt.Errorf("serve SSH: %w", err)
	}
	select {
	case err := <-served:
		return fmt.Errorf("serve SSH: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("shutdown SSH: %w", err)
		}
		// Active sessions get the grace period, then are closed forcibly.
		if err := server.Close(); err != nil {
			return fmt.Errorf("close SSH: %w", err)
		}
	}
	if err := <-served; err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		return fmt.Errorf("serve SSH: %w", err)
	}
	return nil
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stdout, "Usage: ssh-portfolio [-listen host:port] [-host-key path]\n  -listen    SSH listen address (default 127.0.0.1:23234)\n  -host-key  SSH host key path (default .ssh/id_ed25519)")
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runServer(ctx, cfg, log.Default(), 3*time.Second); err != nil {
		log.Fatal(err)
	}
}
