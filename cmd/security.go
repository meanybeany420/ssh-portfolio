package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2/bubbletea"
	gossh "golang.org/x/crypto/ssh"
)

const maxWidth, maxHeight = 320, 120

func newPortfolioProgram(m tea.Model, s ssh.Session) *tea.Program {
	opts := bubbletea.MakeOptions(s)
	// Input can also contain terminal size reports, independently of SSH's
	// window-change requests. Bubble Tea applies this filter before resizing
	// its renderer, whereas model.Update runs too late to protect that path.
	// Append last because MakeOptions installs its own suspend-only filter.
	opts = append(opts, tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			return tea.WindowSizeMsg{Width: boundedDimension(msg.Width, maxWidth), Height: boundedDimension(msg.Height, maxHeight)}
		case tea.SuspendMsg:
			return tea.ResumeMsg{}
		}
		return msg
	}))
	return tea.NewProgram(m, opts...)
}

type terminalRequest struct {
	Term                                     string
	Width, Height, WidthPixels, HeightPixels uint32
	Modes                                    string
}
type resizeRequest struct{ Width, Height, WidthPixels, HeightPixels uint32 }

func validDimensions(w, h uint32) bool  { return w >= 1 && w <= maxWidth && h >= 1 && h <= maxHeight }
func boundedDimension(n, limit int) int { return max(1, min(n, limit)) }

type terminalState struct {
	pty     ssh.Pty // immutable after the initial request, before the shell starts
	windows chan ssh.Window
	context context.Context
}
type terminalKey struct{}

type connectionKey struct{}
type connectionBudget struct {
	session  atomic.Bool
	channels atomic.Uint32
	globals  atomic.Uint32
}

type safeSession struct {
	ssh.Session
	terminal *terminalState
}

func (s safeSession) Pty() (ssh.Pty, <-chan ssh.Window, bool) {
	return s.terminal.pty, s.terminal.windows, true
}
func (s safeSession) Environ() []string { return nil }
func (s safeSession) Context() ssh.Context {
	return sessionContext{Context: s.Session.Context(), local: s.terminal.context}
}

// ssh.Context carries connection metadata. Override its context.Context part
// so middleware also stops when this channel closes, not only the TCP socket.
type sessionContext struct {
	ssh.Context
	local context.Context
}

func (c sessionContext) Deadline() (time.Time, bool) { return c.local.Deadline() }
func (c sessionContext) Done() <-chan struct{}       { return c.local.Done() }
func (c sessionContext) Err() error                  { return c.local.Err() }
func (c sessionContext) Value(key any) any           { return c.local.Value(key) }

// Filter requests before DefaultSessionHandler parses or mutates session state.
// SessionRequestCallback alone does not see env, agent or resize requests.
type shellChannel struct {
	gossh.NewChannel
	ctx      ssh.Context
	terminal *terminalState
	conn     *gossh.ServerConn
}

func (c shellChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	ch, requests, err := c.NewChannel.Accept()
	if err != nil {
		return nil, nil, err
	}
	allowed := make(chan *gossh.Request)
	go func() {
		defer close(allowed)
		pty, shell := false, false
		count := 0
		for {
			select {
			case <-c.ctx.Done():
				return
			case req, ok := <-requests:
				if !ok {
					return
				}
				count++
				if count > 512 || len(req.Payload) > 4096 {
					_ = c.conn.Close()
					return
				}
				accept := false
				switch req.Type {
				case "pty-req":
					var p terminalRequest
					accept = !pty && !shell && len(req.Payload) <= 4096 && gossh.Unmarshal(req.Payload, &p) == nil && validDimensions(p.Width, p.Height)
					if accept {
						pty = true
						// An emulated read-only menu needs neither arbitrary TERM nor terminal modes.
						p.Term = "xterm-256color"
						p.Modes = "\x00"
						p.WidthPixels = 0
						p.HeightPixels = 0
						c.terminal.pty = ssh.Pty{Term: p.Term, Window: ssh.Window{Width: int(p.Width), Height: int(p.Height)}}
						req.Payload = gossh.Marshal(p)
					}
				case "shell":
					accept = pty && !shell && len(req.Payload) == 0
					if accept {
						shell = true
					}
				case "window-change":
					var w resizeRequest
					ok := pty && gossh.Unmarshal(req.Payload, &w) == nil && validDimensions(w.Width, w.Height)
					if ok {
						// Coalesce instead of blocking behind rendering. Never mutate the
						// library session's PTY concurrently with Bubble Tea's initial read.
						select {
						case <-c.terminal.windows:
						default:
						}
						c.terminal.windows <- ssh.Window{Width: int(w.Width), Height: int(w.Height)}
					}
					_ = req.Reply(ok, nil)
					continue
				}
				if !accept {
					_ = req.Reply(false, nil)
					continue
				}
				select {
				case allowed <- req:
				case <-c.ctx.Done():
					return
				}
			}
		}
	}()
	input := &budgetChannel{Channel: ch, conn: c.conn, chunks: make(chan []byte, 8)}
	go input.pump()
	// Stderr is a distinct inbound stream: Channel.Read never sees it.
	// This terminal accepts no inbound extended data, even before PTY/shell.
	go func() {
		var buf [1]byte
		if n, _ := ch.Stderr().Read(buf[:]); n > 0 {
			_ = c.conn.Close()
		}
	}()
	return input, allowed, nil
}

// Input has a lifetime byte budget, not a buffer that can grow with a paste.
type budgetChannel struct {
	gossh.Channel
	conn    *gossh.ServerConn
	chunks  chan []byte
	pending []byte
}

func (c *budgetChannel) pump() {
	defer close(c.chunks)
	total := 0
	for {
		buf := make([]byte, 1024)
		n, err := c.Channel.Read(buf)
		total += n
		if total > 64*1024 {
			_ = c.conn.Close()
			return
		}
		if n > 0 {
			select {
			case c.chunks <- buf[:n]:
			default: // Bounded backlog: do not let input overwhelm rendering.
				_ = c.conn.Close()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *budgetChannel) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.pending) == 0 {
		var ok bool
		c.pending, ok = <-c.chunks
		if !ok {
			return 0, io.EOF
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func shellOnly(s *ssh.Server) error {
	// Preserve existing RSA key material while disabling its legacy SHA-1
	// signature. The library handles negotiation and signing; no custom crypto.
	for i, key := range s.HostSigners {
		if key.PublicKey().Type() != gossh.KeyAlgoRSA {
			continue
		}
		signer, ok := key.(gossh.AlgorithmSigner)
		if !ok {
			return fmt.Errorf("RSA host key does not support algorithm selection")
		}
		restricted, err := gossh.NewSignerWithAlgorithms(signer, []string{gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256})
		if err != nil {
			return fmt.Errorf("restrict RSA host signature: %w", err)
		}
		s.HostSigners[i] = restricted
	}
	// Use the installed library's supported set, not legacy compatibility
	// defaults (which still include SHA-1 KEX and truncated SHA-1 MACs).
	s.ServerConfigCallback = func(ssh.Context) *gossh.ServerConfig {
		algorithms := gossh.SupportedAlgorithms()
		return &gossh.ServerConfig{
			Config:                  gossh.Config{KeyExchanges: algorithms.KeyExchanges, Ciphers: algorithms.Ciphers, MACs: algorithms.MACs},
			PublicKeyAuthAlgorithms: algorithms.PublicKeyAuths,
		}
	}
	s.HandshakeTimeout = 5 * time.Second
	s.IdleTimeout = time.Minute
	s.MaxTimeout = 10 * time.Minute
	s.ConnCallback = func(ctx ssh.Context, c net.Conn) net.Conn {
		ctx.SetValue(connectionKey{}, &connectionBudget{})
		return c
	}
	handler := s.Handler
	s.Handler = func(sess ssh.Session) {
		handler(safeSession{Session: sess, terminal: sess.Context().Value(terminalKey{}).(*terminalState)})
	}
	channelHandler := func(s *ssh.Server, conn *gossh.ServerConn, ch gossh.NewChannel, ctx ssh.Context) {
		budget := ctx.Value(connectionKey{}).(*connectionBudget)
		if budget.channels.Add(1) > 32 {
			_ = conn.Close()
			return
		}
		if ch.ChannelType() != "session" || len(ch.ExtraData()) != 0 {
			_ = ch.Reject(gossh.UnknownChannelType, "unsupported channel")
			return
		}
		if !ctx.Value(connectionKey{}).(*connectionBudget).session.CompareAndSwap(false, true) {
			_ = ch.Reject(gossh.ResourceShortage, "one session per connection")
			return
		}
		local, cancel := context.WithCancel(ctx)
		defer cancel()
		terminal := &terminalState{windows: make(chan ssh.Window, 1), context: local}
		ctx.SetValue(terminalKey{}, terminal)
		ssh.DefaultSessionHandler(s, conn, shellChannel{NewChannel: ch, ctx: ctx, terminal: terminal, conn: conn}, ctx)
	}
	s.ChannelHandlers = map[string]ssh.ChannelHandler{"session": channelHandler, "default": channelHandler}
	s.RequestHandlers = map[string]ssh.RequestHandler{"default": func(ctx ssh.Context, _ *ssh.Server, req *gossh.Request) (bool, []byte) {
		if ctx.Value(connectionKey{}).(*connectionBudget).globals.Add(1) > 16 || len(req.Payload) > 4096 {
			_ = ctx.Value(ssh.ContextKeyConn).(*gossh.ServerConn).Close()
		}
		return false, nil
	}}
	s.SubsystemHandlers = map[string]ssh.SubsystemHandler{}
	s.SessionRequestCallback = func(sess ssh.Session, kind string) bool { _, _, pty := sess.Pty(); return kind == "shell" && pty }
	return nil
}

// Admission is synchronous, before Serve launches a handshake goroutine.
// The library tracks only authenticated connections, not raw sockets.
type limitedListener struct {
	net.Listener
	mu          sync.Mutex
	connections map[*countedConn]string
	perIP       map[string]int
	closed      bool
}

func newLimitedListener(l net.Listener) *limitedListener {
	return &limitedListener{Listener: l, connections: make(map[*countedConn]string), perIP: make(map[string]int)}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip, _, err := net.SplitHostPort(c.RemoteAddr().String())
		if err != nil {
			_ = c.Close()
			continue
		}
		l.mu.Lock()
		if l.closed || len(l.connections) >= 32 || l.perIP[ip] >= 4 {
			l.mu.Unlock()
			_ = c.Close()
			continue
		}
		tracked := &countedConn{Conn: c, owner: l}
		l.connections[tracked] = ip
		l.perIP[ip]++
		l.mu.Unlock()
		return tracked, nil
	}
}

func (l *limitedListener) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return l.Listener.Close()
}

func (l *limitedListener) closeConnections() {
	l.mu.Lock()
	connections := make([]*countedConn, 0, len(l.connections))
	for c := range l.connections {
		connections = append(connections, c)
	}
	l.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
}

// This lifetime ingress limit includes the SSH identification/handshake and
// all later encrypted packets (data, unknown extended types, control and rekey).
// 2 MiB leaves headroom for supported handshakes while bounding traffic that
// the SSH Channel API cannot expose. It is separate from the 64 KiB UI budget.
const maxIncomingTransportBytes = 2 * 1024 * 1024

type countedConn struct {
	net.Conn
	owner *limitedListener
	once  sync.Once

	readMu   sync.Mutex // Serialize reads and budget accounting, not Close.
	incoming int        // Always in [0, maxIncomingTransportBytes].
}

func (c *countedConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	remaining := maxIncomingTransportBytes - c.incoming
	if remaining == 0 {
		_ = c.Close()
		return 0, io.EOF
	}
	// Bound the actual socket read, not just the returned count: no byte
	// beyond the limit can reach the library parser. The bounded addition
	// below cannot overflow, even over a long-lived connection.
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := c.Conn.Read(p)
	c.incoming += n
	if c.incoming == maxIncomingTransportBytes {
		_ = c.Close() // Immediately release admission, without another Read.
		return n, io.EOF
	}
	return n, err
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		l := c.owner
		l.mu.Lock()
		defer l.mu.Unlock()
		ip := l.connections[c]
		delete(l.connections, c)
		l.perIP[ip]--
		if l.perIP[ip] == 0 {
			delete(l.perIP, ip)
		}
	})
	return err
}
