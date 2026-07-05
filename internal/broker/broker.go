//go:build linux

// Package broker fronts interactive shell sessions so that oubliette -- not
// sshd -- owns both the pty and the client connection from the very start.
//
// It listens for TCP connections, gives each one a shell on a pty it allocates,
// and watches the client's input for a trigger pattern. When the trap springs
// -- either from that in-band pattern or from an external containment request
// on the control socket -- it migrates the shell's process tree into the shadow
// VM and re-points the client connection at the migrated copy, so the session
// continues inside the guest without the client observing a break.
//
// Because the broker owns the pty master and the client socket, killing the
// source shell during migration tears down nothing the client can see. That is
// what makes the handoff seamless -- the failure mode that dooms reusing a real
// sshd-owned terminal does not arise here.
package broker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/oubliette/oubliette/internal/config"
	"github.com/oubliette/oubliette/internal/control"
	"github.com/oubliette/oubliette/internal/migrate"
	"github.com/oubliette/oubliette/internal/pty"
	"github.com/oubliette/oubliette/internal/ptymux"
)

// readBufSize bounds a single read from the client connection.
const readBufSize = 4096

// Config parameterizes the broker.
type Config struct {
	// Listen is the TCP address to accept sessions on, e.g. ":2222".
	Listen string
	// Shell is the program spawned for each session, e.g. "/bin/bash".
	Shell string
	// Trigger is a substring in the client's input that springs the trap. It
	// must be non-empty.
	Trigger string
	// VMName is the shadow VM domain to migrate sessions into.
	VMName string
	// Migration holds the CRIU/transfer parameters passed to the migrate engine.
	Migration *config.Config
	// ControlSocket, if non-empty, is the path of a Unix socket on which the
	// broker accepts external containment requests (see internal/control).
	ControlSocket string
}

// Broker fronts and traps shell sessions. It tracks live sessions so an
// external containment request can spring the trap on a specific one.
type Broker struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*session // keyed by client remote address
}

// New returns a Broker for cfg.
func New(cfg Config) *Broker {
	return &Broker{cfg: cfg, sessions: make(map[string]*session)}
}

// session is one fronted client. Its trap can be sprung by the in-band trigger
// scanner or by an external control request; either path funnels through
// springTrap so it fires exactly once.
type session struct {
	remote   string
	conn     net.Conn
	master   *os.File
	shellPID int

	trapOnce sync.Once
	trapCh   chan struct{}
}

// springTrap marks the session for migration and interrupts the pre-migration
// client read (via a read deadline) so it stops promptly. Idempotent.
func (s *session) springTrap() {
	s.trapOnce.Do(func() {
		close(s.trapCh)
		_ = s.conn.SetReadDeadline(time.Now())
	})
}

// Serve is a convenience wrapper: New(cfg).Serve(ctx).
func Serve(ctx context.Context, cfg Config) error {
	return New(cfg).Serve(ctx)
}

// Serve listens for client sessions (and, if configured, external control
// requests) until ctx is done, then waits for outstanding sessions to finish.
func (b *Broker) Serve(ctx context.Context) error {
	if b.cfg.Trigger == "" {
		return fmt.Errorf("broker: trigger must be non-empty")
	}

	if b.cfg.ControlSocket != "" {
		go func() {
			if err := control.Serve(ctx, b.cfg.ControlSocket, b); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "control server exited", "err", err)
			}
		}()
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", b.cfg.Listen)
	if err != nil {
		return fmt.Errorf("broker: listen on %s: %w", b.cfg.Listen, err)
	}
	defer ln.Close()
	slog.InfoContext(ctx, "broker listening", "addr", b.cfg.Listen, "shell", b.cfg.Shell, "trigger", b.cfg.Trigger, "vm", b.cfg.VMName)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.WarnContext(ctx, "broker accept failed", "err", err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if herr := b.handleConn(ctx, conn); herr != nil {
				slog.WarnContext(ctx, "broker session ended with error", "remote", conn.RemoteAddr().String(), "err", herr)
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// ContainRemote springs the trap on the session whose client is
// remoteIP:remotePort. It satisfies control.Container.
func (b *Broker) ContainRemote(remoteIP string, remotePort int) error {
	key := net.JoinHostPort(remoteIP, strconv.Itoa(remotePort))
	b.mu.Lock()
	s := b.sessions[key]
	b.mu.Unlock()
	if s == nil {
		return fmt.Errorf("no active session from %s", key)
	}
	s.springTrap()
	return nil
}

// ContainPID springs the trap on the brokered session whose shell has the given
// pid. It satisfies control.Container. (Containing an arbitrary, non-brokered
// pid is a separate, future capability.)
func (b *Broker) ContainPID(pid int) error {
	b.mu.Lock()
	var found *session
	for _, s := range b.sessions {
		if s.shellPID == pid {
			found = s
			break
		}
	}
	b.mu.Unlock()
	if found == nil {
		return fmt.Errorf("no brokered session with pid %d", pid)
	}
	found.springTrap()
	return nil
}

func (b *Broker) register(s *session) {
	b.mu.Lock()
	b.sessions[s.remote] = s
	b.mu.Unlock()
}

func (b *Broker) deregister(s *session) {
	b.mu.Lock()
	if b.sessions[s.remote] == s {
		delete(b.sessions, s.remote)
	}
	b.mu.Unlock()
}

// handleConn runs one client session: spawn a shell on a fresh pty, relay the
// client to it until the trap springs, then migrate the shell tree into the
// guest and hand the client over to the migrated copy.
func (b *Broker) handleConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	slog.InfoContext(ctx, "broker session started", "remote", remote)

	master, slave, err := pty.Open()
	if err != nil {
		return fmt.Errorf("open pty: %w", err)
	}
	defer master.Close()

	cmd := exec.Command(b.cfg.Shell)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = shellEnv()
	// Run from a directory that also exists in the guest, so the migrated
	// shell's cwd stays coherent (preflight requires the cwd to exist there).
	cmd.Dir = "/root"
	// New session with the pty slave as controlling terminal, so the shell is a
	// proper session/job-control leader -- and so criu --shell-job has a tty to
	// checkpoint.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		slave.Close()
		return fmt.Errorf("start shell %s: %w", b.cfg.Shell, err)
	}
	shellPID := cmd.Process.Pid
	// The child holds its own copy of the slave; drop ours so pty EOF is
	// observed once the shell (and its descendants) exit.
	slave.Close()
	slog.InfoContext(ctx, "broker spawned shell", "remote", remote, "pid", shellPID, "shell", b.cfg.Shell)

	s := &session{remote: remote, conn: conn, master: master, shellPID: shellPID, trapCh: make(chan struct{})}
	b.register(s)
	defer b.deregister(s)

	// Phase 1: raw relay client <-> local pty, until the trap springs.
	triggered, ptyCopyDone := b.preMigrationRelay(ctx, s)
	if !triggered {
		slog.InfoContext(ctx, "broker session ended before trap", "remote", remote)
		_ = cmd.Wait()
		return nil
	}
	slog.InfoContext(ctx, "trap sprung; containing session", "remote", remote, "pid", shellPID)

	// The pre-migration read was interrupted by the trap's deadline; clear it
	// so the post-migration relay can read the client again.
	_ = conn.SetReadDeadline(time.Time{})

	// Phase 2: migrate the shell's process tree into the shadow VM.
	guestConn, err := migrate.Migrate(ctx, shellPID, b.cfg.VMName, b.cfg.Migration)
	if err != nil {
		return fmt.Errorf("migrate shell tree: %w", err)
	}
	defer guestConn.Close()
	// The local shell is gone (criu killed the tree). Release the local pty,
	// wait for the Phase-1 copier to observe EOF and stop (so it cannot write
	// to the client concurrently with the Phase-3 relay), and reap the shell.
	master.Close()
	<-ptyCopyDone
	go func() { _ = cmd.Wait() }()
	slog.InfoContext(ctx, "shell migrated into shadow vm; bridging client to guest", "remote", remote)

	// Phase 3: relay client <-> guest pty. The guest side is framed (ptymux) so
	// it can carry window-size updates; the client side is raw. nil onWinsize:
	// the broker never receives winsize frames from the guest.
	relay := ptymux.NewRelay(conn, guestConn, nil)
	if err := relay.Run(ctx); err != nil {
		return fmt.Errorf("client bridge: %w", err)
	}
	slog.InfoContext(ctx, "broker session complete", "remote", remote)
	return nil
}

// preMigrationRelay copies bytes between the client and the local pty master,
// scanning the client->pty direction for the in-band trigger. It returns
// triggered=true once the trap springs (via the scanner or an external
// control request), or false if the session ends first.
//
// The returned channel is closed once the pty->client copier goroutine has
// fully exited; the caller waits on it (after closing the pty) before starting
// the post-migration relay, so the two never write to the client at once.
func (b *Broker) preMigrationRelay(ctx context.Context, s *session) (triggered bool, ptyCopyDone <-chan struct{}) {
	ended := make(chan struct{})
	var endOnce sync.Once
	end := func() { endOnce.Do(func() { close(ended) }) }

	copyDone := make(chan struct{})
	// pty -> client
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(s.conn, s.master)
		end()
	}()

	// client -> pty, scanning for the in-band trigger
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		sc := &triggerScanner{pat: []byte(b.cfg.Trigger)}
		buf := make([]byte, readBufSize)
		for {
			n, err := s.conn.Read(buf)
			if n > 0 {
				if _, werr := s.master.Write(buf[:n]); werr != nil {
					end()
					return
				}
				if sc.feed(buf[:n]) {
					s.springTrap()
					return
				}
			}
			if err != nil {
				// A trap sets a read deadline to interrupt this read; that is
				// not a real end-of-session, so distinguish it.
				select {
				case <-s.trapCh:
					return
				default:
					end()
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return false, copyDone
	case <-s.trapCh:
		// Wait for the reader to stop touching the client socket before the
		// caller clears the read deadline and starts the post-migration relay,
		// so the two never read the client at once.
		<-readerDone
		return true, copyDone
	case <-ended:
		return false, copyDone
	}
}

// shellEnv builds the environment for a spawned shell. It drops any inherited
// locale variables (and TERM/HOME/PWD, which we set explicitly) and forces the
// C locale: in C, glibc serves locale data from built-in tables rather than
// mmapping /usr/lib/locale/* files, so the migrated shell has no locale-file
// memory mappings to reconcile against the guest -- avoiding a coherence
// mismatch that otherwise fails criu restore. Inherited vars must be removed
// rather than shadowed, since getenv returns the first match, not the last.
func shellEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "LANG="),
			strings.HasPrefix(kv, "LC_"),
			strings.HasPrefix(kv, "TERM="),
			strings.HasPrefix(kv, "HOME="),
			strings.HasPrefix(kv, "PWD="):
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TERM=xterm", "HOME=/root", "LANG=C", "LC_ALL=C")
}

// triggerScanner reports whether a byte pattern has appeared in a stream fed to
// it in arbitrary chunks. It retains the last len(pat)-1 bytes between feeds so
// a match spanning a chunk boundary is still detected.
type triggerScanner struct {
	pat  []byte
	tail []byte
}

func (s *triggerScanner) feed(b []byte) bool {
	if len(s.pat) == 0 {
		return false
	}
	hay := make([]byte, 0, len(s.tail)+len(b))
	hay = append(hay, s.tail...)
	hay = append(hay, b...)
	if bytes.Contains(hay, s.pat) {
		s.tail = nil
		return true
	}
	keep := len(s.pat) - 1
	if keep > len(hay) {
		keep = len(hay)
	}
	s.tail = append([]byte(nil), hay[len(hay)-keep:]...)
	return false
}
