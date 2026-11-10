package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func livePortfolio(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan string, 1), make(chan error, 1)
	keyPath := filepath.Join(t.TempDir(), "key")
	go func() {
		done <- runServer(ctx, config{listen: "127.0.0.1:0", hostKey: keyPath}, log.New(readinessLog{ready}, "", 0), 20*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("shutdown hung")
		}
	})
	select {
	case address := <-ready:
		return address
	case <-time.After(5 * time.Second):
		t.Fatal("startup hung")
	}
	return ""
}

func dialPortfolio(t *testing.T, address string) *gossh.Client {
	t.Helper()
	c, err := gossh.Dial("tcp", address, &gossh.ClientConfig{User: "visitor", HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestLiveShellOnlyRequests(t *testing.T) {
	address := livePortfolio(t)
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"exec", gossh.Marshal(struct{ Command string }{""})},
		{"exec", gossh.Marshal(struct{ Command string }{"id"})},
		{"env", gossh.Marshal(struct{ Key, Value string }{"TERM", "evil"})},
		{"auth-agent-req@openssh.com", nil},
		{"x11-req", nil}, {"subsystem", gossh.Marshal(struct{ Value string }{"sftp"})},
		{"shell", nil}, // no PTY must not enter middleware
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dialPortfolio(t, address)
			ch, reqs, err := c.OpenChannel("session", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ch.Close()
			go gossh.DiscardRequests(reqs)
			go io.Copy(io.Discard, ch)
			ok, err := ch.SendRequest(tc.name, true, tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatalf("forbidden %s accepted", tc.name)
			}
		})
	}
}

type ptyPayload struct {
	Term                                     string
	Width, Height, WidthPixels, HeightPixels uint32
	Modes                                    string
}
type windowPayload struct{ Width, Height, WidthPixels, HeightPixels uint32 }

func TestLiveDimensionsBeforeMiddleware(t *testing.T) {
	address := livePortfolio(t)
	for _, payload := range [][]byte{
		gossh.Marshal(ptyPayload{"xterm", ^uint32(0), ^uint32(0), 0, 0, "\x00"}),
		gossh.Marshal(ptyPayload{"xterm", 0, 0, 0, 0, "\x00"}),
		[]byte{0, 0, 0, 255},
	} {
		c := dialPortfolio(t, address)
		ch, reqs, err := c.OpenChannel("session", nil)
		if err != nil {
			t.Fatal(err)
		}
		go gossh.DiscardRequests(reqs)
		ok, err := ch.SendRequest("pty-req", true, payload)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("invalid/oversized PTY accepted")
		}
		ch.Close()
	}
	c := dialPortfolio(t, address)
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go gossh.DiscardRequests(reqs)
	go io.Copy(io.Discard, ch)
	for _, req := range []struct {
		kind    string
		payload []byte
	}{
		{"pty-req", gossh.Marshal(ptyPayload{"xterm-256color", 120, 40, 0, 0, "\x00"})},
		{"shell", nil},
		{"window-change", gossh.Marshal(windowPayload{160, 50, 0, 0})},
	} {
		ok, err := ch.SendRequest(req.kind, true, req.payload)
		if err != nil || !ok {
			t.Fatalf("valid %s rejected: %v", req.kind, err)
		}
	}
	ok, err := ch.SendRequest("window-change", true, gossh.Marshal(windowPayload{^uint32(0), ^uint32(0), 0, 0}))
	if err != nil || ok {
		t.Fatalf("huge resize: accepted=%v err=%v", ok, err)
	}
}

func TestModelBoundsDimensions(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 1 << 30, Height: 1 << 30}, {Width: -1, Height: -1}} {
		next, _ := newModel().Update(size)
		m := next.(model)
		if m.width < 1 || m.width > 320 || m.height < 1 || m.height > 120 {
			t.Errorf("unsafe model dimensions: %+v", m)
		}
	}
}

func TestLiveOneChannelPerConnection(t *testing.T) {
	c := dialPortfolio(t, livePortfolio(t))
	ch, requests, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go gossh.DiscardRequests(requests)
	for i := 0; i < 3; i++ {
		extra, _, err := c.OpenChannel("session", nil)
		if err == nil {
			extra.Close()
			t.Error("extra session channel accepted")
		}
	}
	ch.Close()
	extra, _, err := c.OpenChannel("session", nil)
	if err == nil {
		extra.Close()
		t.Error("channel lifetime budget reset after close")
	}
}

func TestLiveRawConnectionPerIPLimit(t *testing.T) {
	address := livePortfolio(t)
	for i := 0; i < 4; i++ {
		c, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
			t.Fatalf("allowed raw connection %d: %v", i, err)
		}
	}
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if line, err := bufio.NewReader(c).ReadString('\n'); err == nil {
		t.Fatalf("excess pre-handshake connection admitted: %q", line)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("excess raw connection not promptly closed")
	}
}

func TestLiveRequestFloodDisconnects(t *testing.T) {
	c := dialPortfolio(t, livePortfolio(t))
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go gossh.DiscardRequests(reqs)
	for i := 0; i < 514; i++ {
		if _, err := ch.SendRequest("env", true, nil); err != nil {
			return
		}
	}
	t.Fatal("session request flood remained connected")
}

func TestLiveGlobalFloodDisconnects(t *testing.T) {
	c := dialPortfolio(t, livePortfolio(t))
	for i := 0; i < 18; i++ {
		ok, _, err := c.SendRequest("tcpip-forward", true, nil)
		if err != nil {
			return
		}
		if ok {
			t.Fatal("global forwarding accepted")
		}
	}
	t.Fatal("global request flood remained connected")
}

func TestLiveChannelFloodDisconnects(t *testing.T) {
	c := dialPortfolio(t, livePortfolio(t))
	for i := 0; i < 34; i++ {
		ch, _, err := c.OpenChannel("direct-tcpip", nil)
		if err == nil {
			ch.Close()
			t.Fatal("forward channel accepted")
		}
		if _, ok := err.(*gossh.OpenChannelError); !ok {
			return
		}
	}
	t.Fatal("channel flood remained connected")
}

func TestLiveRejectsInboundStderr(t *testing.T) {
	for _, stage := range []string{"before-pty", "after-pty", "after-shell"} {
		t.Run(stage, func(t *testing.T) {
			address, l := testPolicyServer(t, nil)
			c := dialPortfolio(t, address)
			ch, reqs, err := c.OpenChannel("session", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ch.Close()
			go gossh.DiscardRequests(reqs)
			go io.Copy(io.Discard, ch)
			if stage != "before-pty" {
				ok, err := ch.SendRequest("pty-req", true, gossh.Marshal(ptyPayload{"xterm", 80, 24, 0, 0, "\x00"}))
				if err != nil || !ok {
					t.Fatalf("PTY: accepted=%v err=%v", ok, err)
				}
			}
			if stage == "after-shell" {
				if ok, err := ch.SendRequest("shell", true, nil); err != nil || !ok {
					t.Fatalf("shell: accepted=%v err=%v", ok, err)
				}
			}
			// Even one byte on the unsupported inbound stderr stream must
			// disconnect, independently of the ordinary terminal input limit.
			if _, err := ch.Stderr().Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("inbound stderr did not promptly disconnect")
			}
			waitForAdmissionRelease(t, l)
		})
	}
}

func TestLiveUnknownExtendedFloodDisconnects(t *testing.T) {
	for _, stage := range []string{"before-pty", "after-pty", "after-shell"} {
		t.Run(stage, func(t *testing.T) {
			address, l := testPolicyServer(t, nil)
			c := dialPortfolio(t, address)
			ch, reqs, err := c.OpenChannel("session", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ch.Close()
			go gossh.DiscardRequests(reqs)
			go io.Copy(io.Discard, ch)
			if stage != "before-pty" {
				ok, err := ch.SendRequest("pty-req", true, gossh.Marshal(ptyPayload{"xterm", 80, 24, 0, 0, "\x00"}))
				if err != nil || !ok {
					t.Fatalf("PTY: accepted=%v err=%v", ok, err)
				}
			}
			if stage == "after-shell" {
				if ok, err := ch.SendRequest("shell", true, nil); err != nil || !ok {
					t.Fatalf("shell: accepted=%v err=%v", ok, err)
				}
			}
			// Channel exposes only Read/Stderr, but the installed concrete
			// client channel supports sending otherwise unobservable types.
			writer, ok := ch.(interface {
				WriteExtended([]byte, uint32) (int, error)
			})
			if !ok {
				t.Fatal("installed SSH client lacks extended-data writer")
			}
			written := make(chan error, 1)
			go func() {
				chunk := make([]byte, 16*1024)
				for i := 0; i < 256; i++ { // 4 MiB, with automatic window credits.
					if _, err := writer.WriteExtended(chunk, 2); err != nil {
						written <- err
						return
					}
				}
				written <- nil
			}()
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("4 MiB unknown extended flood did not disconnect")
			}
			select {
			case <-written:
			case <-time.After(time.Second):
				t.Fatal("disconnected extended-data writer remained blocked")
			}
			waitForAdmissionRelease(t, l)
			replacement := dialPortfolio(t, address)
			if _, _, err := replacement.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				t.Fatalf("replacement connection: %v", err)
			}
		})
	}
}

func TestCountedConnTransportBudgetBeforeHandshake(t *testing.T) {
	// Read directly from the admitted raw socket, without starting SSH.
	// The parser must never receive more than 2 MiB, even if a final read
	// spans the boundary or concurrent callers compete for the remainder.
	for _, readers := range []int{1, 4} {
		t.Run(fmt.Sprintf("readers-%d", readers), func(t *testing.T) {
			raw, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			l := newLimitedListener(raw)
			defer l.Close()
			defer l.closeConnections()
			client, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server, err := l.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			if err := server.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			written := make(chan struct{})
			go func() {
				defer close(written)
				_, _ = io.CopyN(client, zeroReader{}, 3*1024*1024)
			}()
			totals := make(chan int, readers)
			for i := 0; i < readers; i++ {
				go func() {
					buf := make([]byte, 70001) // Deliberately not a divisor of the limit.
					total := 0
					for {
						n, err := server.Read(buf)
						total += n
						if err != nil || total == 2*1024*1024 {
							// In the single-reader case, do not issue a further
							// Read at the exact limit: release must be immediate.
							totals <- total
							return
						}
					}
				}()
			}
			total := 0
			for i := 0; i < readers; i++ {
				total += <-totals
			}
			if total != 2*1024*1024 {
				t.Fatalf("raw bytes delivered to parser = %d, want exactly 2097152", total)
			}
			waitForAdmissionRelease(t, l)
			if n, err := server.Read(make([]byte, 1)); n != 0 || err == nil {
				t.Fatalf("exhausted reader: n=%d err=%v", n, err)
			}
			client.Close()
			<-written
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func waitForAdmissionRelease(t *testing.T, l *limitedListener) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		l.mu.Lock()
		n, ips := len(l.connections), len(l.perIP)
		l.mu.Unlock()
		if n == 0 && ips == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("admission leaked: connections=%d IPs=%d", n, ips)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLiveInputBudget(t *testing.T) {
	c := dialPortfolio(t, livePortfolio(t))
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	s.Stdout, s.Stderr = io.Discard, io.Discard
	if err := s.RequestPty("xterm", 40, 120, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	// Unknown keys do not quit the menu. The budget must close it.
	go func() { _, _ = io.WriteString(input, strings.Repeat("x", 65537)) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("input budget did not disconnect")
	}
}

func testPolicyServer(t *testing.T, configure func(*ssh.Server)) (string, *limitedListener) {
	t.Helper()
	s, err := newPortfolioServer(config{listen: "127.0.0.1:0", hostKey: filepath.Join(t.TempDir(), "key")})
	if err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(s)
	}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newLimitedListener(raw)
	ready := &readyListener{Listener: l, ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ready) }()
	<-ready.ready
	t.Cleanup(func() {
		s.Close()
		l.closeConnections()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	return l.Addr().String(), l
}

func rawVisitor(t *testing.T, address, ip string) net.Conn {
	t.Helper()
	d := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	c, err := d.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
		c.Close()
		t.Fatal(err)
	}
	return c
}

func TestLiveTotalRawLimitAndRelease(t *testing.T) {
	address, l := testPolicyServer(t, nil)
	var connections []net.Conn
	defer func() {
		for _, c := range connections {
			c.Close()
		}
	}()
	for i := 0; i < 32; i++ {
		c := rawVisitor(t, address, fmt.Sprintf("127.0.0.%d", 2+i/4))
		connections = append(connections, c)
	}
	d := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.20")}}
	denied, err := d.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer denied.Close()
	denied.SetReadDeadline(time.Now().Add(time.Second))
	if line, err := bufio.NewReader(denied).ReadString('\n'); err == nil {
		t.Fatalf("total limit admitted extra raw connection: %q", line)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("total limit did not promptly close socket")
	}
	var wg sync.WaitGroup
	for _, c := range connections {
		wg.Add(1)
		go func(c net.Conn) { defer wg.Done(); c.Close(); c.Close() }(c)
	}
	wg.Wait()
	deadline := time.Now().Add(time.Second)
	for {
		l.mu.Lock()
		count, ips := len(l.connections), len(l.perIP)
		l.mu.Unlock()
		if count == 0 && ips == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("raw admission leaked: connections=%d IPs=%d", count, ips)
		}
		time.Sleep(time.Millisecond)
	}
	replacement := rawVisitor(t, address, "127.0.0.2")
	replacement.Close()
}

func TestLiveTimeouts(t *testing.T) {
	for _, kind := range []string{"handshake", "idle", "absolute"} {
		t.Run(kind, func(t *testing.T) {
			address, l := testPolicyServer(t, func(s *ssh.Server) {
				if s.HandshakeTimeout != 5*time.Second || s.IdleTimeout != time.Minute || s.MaxTimeout != 10*time.Minute {
					t.Fatalf("unexpected production timeouts: %v %v %v", s.HandshakeTimeout, s.IdleTimeout, s.MaxTimeout)
				}
				s.HandshakeTimeout = time.Second
				s.IdleTimeout = time.Second
				s.MaxTimeout = time.Second
				switch kind {
				case "handshake":
					s.HandshakeTimeout = 80 * time.Millisecond
				case "idle":
					s.IdleTimeout = 80 * time.Millisecond
				case "absolute":
					s.MaxTimeout = 150 * time.Millisecond
				}
			})
			if kind == "handshake" {
				c := rawVisitor(t, address, "127.0.0.1")
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(2 * time.Second))
				if _, err := c.Read(make([]byte, 1)); err == nil {
					t.Fatal("handshake timeout not enforced")
				} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
					t.Fatal("client timed out instead of server")
				}
			} else {
				c := dialPortfolio(t, address)
				stop := make(chan struct{})
				defer close(stop)
				if kind == "absolute" {
					go func() {
						ticker := time.NewTicker(20 * time.Millisecond)
						defer ticker.Stop()
						for {
							select {
							case <-stop:
								return
							case <-ticker.C:
								_, _, _ = c.SendRequest("keepalive@openssh.com", true, nil)
							}
						}
					}()
				}
				done := make(chan error, 1)
				go func() { done <- c.Wait() }()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("timeout not enforced")
				}
			}
			deadline := time.Now().Add(time.Second)
			for {
				l.mu.Lock()
				n := len(l.connections)
				l.mu.Unlock()
				if n == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("timeout leaked admission slot")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestLiveDeniedChannelAndGlobalTypes(t *testing.T) {
	address := livePortfolio(t)
	for _, kind := range []string{"direct-tcpip", "forwarded-tcpip", "auth-agent@openssh.com", "x11", "direct-streamlocal@openssh.com", "unknown"} {
		c := dialPortfolio(t, address)
		ch, _, err := c.OpenChannel(kind, nil)
		if err == nil {
			ch.Close()
			t.Errorf("accepted %s", kind)
		}
		c.Close()
	}
	c := dialPortfolio(t, address)
	for _, kind := range []string{"tcpip-forward", "cancel-tcpip-forward", "streamlocal-forward@openssh.com", "keepalive@openssh.com", "unknown"} {
		ok, _, err := c.SendRequest(kind, true, nil)
		if err != nil || ok {
			t.Errorf("global %s accepted=%v err=%v", kind, ok, err)
		}
	}
}

func TestLiveRejectsInsecureTransportAlgorithms(t *testing.T) {
	address := livePortfolio(t)
	for _, tc := range []struct {
		name       string
		algorithms gossh.Config
	}{
		{"sha1-kex", gossh.Config{KeyExchanges: []string{gossh.InsecureKeyExchangeDH14SHA1}}},
		{"truncated-sha1-mac", gossh.Config{Ciphers: []string{gossh.CipherAES128CTR}, MACs: []string{gossh.InsecureHMACSHA196}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := gossh.Dial("tcp", address, &gossh.ClientConfig{Config: tc.algorithms, User: "visitor", HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: time.Second})
			if err == nil {
				c.Close()
				t.Fatal("library-classified insecure transport negotiated")
			}
		})
	}
}

func TestLiveChannelCloseCancelsSessionContext(t *testing.T) {
	started, ended := make(chan struct{}), make(chan struct{})
	address, _ := testPolicyServer(t, func(s *ssh.Server) {
		s.Handler = func(session ssh.Session) { close(started); <-session.Context().Done(); close(ended) }
		if err := shellOnly(s); err != nil {
			t.Fatal(err)
		}
	})
	c := dialPortfolio(t, address)
	session, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RequestPty("xterm", 40, 120, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	session.Close()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("closed channel left handler context alive")
	}
	// Cancellation is local to the session; the connection itself remains usable.
	if _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRSAHostKeyUsesSHA2(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	address, _ := testPolicyServer(t, func(s *ssh.Server) {
		s.HostSigners = []ssh.Signer{signer}
		if err := shellOnly(s); err != nil {
			t.Fatal(err)
		}
	})
	for _, algorithm := range []string{gossh.KeyAlgoRSA, gossh.KeyAlgoRSASHA256, gossh.KeyAlgoRSASHA512} {
		t.Run(algorithm, func(t *testing.T) {
			c, err := gossh.Dial("tcp", address, &gossh.ClientConfig{User: "visitor", HostKeyAlgorithms: []string{algorithm}, HostKeyCallback: gossh.FixedHostKey(signer.PublicKey()), Timeout: time.Second})
			if algorithm == gossh.KeyAlgoRSA {
				if err == nil {
					c.Close()
					t.Fatal("legacy RSA/SHA-1 host signature accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
			}
		})
	}
}

type dimensionProbe struct{ sizes chan tea.WindowSizeMsg }

func (m dimensionProbe) Init() tea.Cmd  { return nil }
func (m dimensionProbe) View() tea.View { return tea.NewView("") }
func (m dimensionProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.sizes <- size
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "q" {
		return m, tea.Quit
	}
	return m, nil
}

func TestLiveTerminalReportDimensionsBeforeRenderer(t *testing.T) {
	sizes := make(chan tea.WindowSizeMsg, 16)
	address, _ := testPolicyServer(t, func(s *ssh.Server) {
		s.Handler = func(session ssh.Session) { _, _ = newPortfolioProgram(dimensionProbe{sizes}, session).Run() }
		if err := shellOnly(s); err != nil {
			t.Fatal(err)
		}
	})
	c := dialPortfolio(t, address)
	session, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	input, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	session.Stdout, session.Stderr = io.Discard, io.Discard
	if err := session.RequestPty("xterm", 40, 120, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	// Terminal data, not an SSH window-change request: the parser turns this
	// report into a WindowSizeMsg before Bubble Tea's renderer sees it.
	if _, err := io.WriteString(input, "\x1b[8;512;1024tq"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case size := <-sizes:
			if size.Width == 120 && size.Height == 40 {
				continue
			}
			if size.Width < 1 || size.Width > 320 || size.Height < 1 || size.Height > 120 {
				t.Fatalf("terminal report reached renderer unbounded: %+v", size)
			}
			if size.Width != 320 || size.Height != 120 {
				t.Fatalf("unexpected bounded report: %+v", size)
			}
			return
		case <-deadline:
			t.Fatal("terminal report was not processed")
		}
	}
}
