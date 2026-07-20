//go:build linux

// Package control exposes a local Unix-domain socket through which an external
// detector (e.g. a Suricata eve.json bridge) can ask oubliette to contain a
// session: checkpoint it and drop it into the shadow VM.
//
// The protocol is line-delimited JSON. Each request line is one command and
// gets one JSON reply line:
//
//	{"action":"contain","match":{"remote_ip":"1.2.3.4","remote_port":44321}}
//	{"action":"contain","pid":1234}
//
// The socket is host-local and mode 0600: "contain" is a privileged verb, so
// it is deliberately not exposed over the network. An external tool that can
// read Suricata alerts translates a flow to one of these requests.
package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
)

// Container enacts containment requests arriving on the control socket.
type Container interface {
	// ContainRemote springs the trap on the session whose client is
	// remoteIP:remotePort. It returns an error if no such session exists.
	ContainRemote(remoteIP string, remotePort int) error
	// ContainPID contains the process tree rooted at pid. It returns an error
	// if the pid cannot be contained.
	ContainPID(pid int) error
}

// request is one control command.
type request struct {
	Action string     `json:"action"`
	Match  *matchSpec `json:"match,omitempty"`
	PID    int        `json:"pid,omitempty"`
}

// matchSpec identifies a session by its client's address.
type matchSpec struct {
	RemoteIP   string `json:"remote_ip"`
	RemotePort int    `json:"remote_port"`
}

// reply is the response to one request.
type reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Serve listens on the Unix socket at socketPath and dispatches containment
// requests to c until ctx is done. A stale socket file at socketPath is
// removed first; the socket is created mode 0600.
func Serve(ctx context.Context, socketPath string, c Container) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("control: remove stale socket %q: %w", socketPath, err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("control: listen %q: %w", socketPath, err)
	}
	defer ln.Close()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("control: chmod socket %q: %w", socketPath, err)
	}
	slog.InfoContext(ctx, "control socket listening", "path", socketPath)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.WarnContext(ctx, "control accept failed", "err", err)
			continue
		}
		go handleConn(ctx, conn, c)
	}
}

// handleConn reads line-delimited requests from conn and writes a reply per line.
func handleConn(ctx context.Context, conn net.Conn, c Container) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		rep := dispatch(ctx, line, c)
		out, _ := json.Marshal(rep)
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// dispatch parses one request line and invokes the matching containment action.
func dispatch(ctx context.Context, line []byte, c Container) reply {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return reply{OK: false, Error: fmt.Sprintf("bad json: %v", err)}
	}
	if req.Action != "contain" {
		return reply{OK: false, Error: fmt.Sprintf("unknown action %q", req.Action)}
	}
	switch {
	case req.Match != nil:
		if err := c.ContainRemote(req.Match.RemoteIP, req.Match.RemotePort); err != nil {
			return reply{OK: false, Error: err.Error()}
		}
		slog.InfoContext(ctx, "contain requested by remote", "ip", req.Match.RemoteIP, "port", req.Match.RemotePort)
		return reply{OK: true}
	case req.PID > 0:
		if err := c.ContainPID(req.PID); err != nil {
			return reply{OK: false, Error: err.Error()}
		}
		slog.InfoContext(ctx, "contain requested by pid", "pid", req.PID)
		return reply{OK: true}
	default:
		return reply{OK: false, Error: "contain requires either match or pid"}
	}
}
