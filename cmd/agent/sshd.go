package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

// newSSHConfig creates a server config that accepts any connection without
// requiring a key. The WireGuard tunnel is the authentication layer — only
// users with a valid Igloo session can reach this server.
func newSSHConfig() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		NoClientAuth: true,
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("[ssh] failed to generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		log.Fatalf("[ssh] failed to create signer: %v", err)
	}
	cfg.AddHostKey(signer)
	return cfg
}

// serveSSH handles a single raw TCP connection as an SSH session.
func serveSSH(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			return
		}
		go handleSSHSession(ch, requests)
	}
}

// serveSSHOnLocalhost listens on 127.0.0.1:2222 for the WebSocket proxy path.
func serveSSHOnLocalhost(cfg *ssh.ServerConfig) {
	ln, err := net.Listen("tcp", "127.0.0.1:2222")
	if err != nil {
		log.Printf("[ssh] failed to listen on :2222: %v", err)
		return
	}
	log.Printf("[ssh] embedded SSH server on 127.0.0.1:2222")
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveSSH(conn, cfg)
	}
}

func handleSSHSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()

	var (
		ptmx *os.File
		mu   sync.Mutex
	)

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			_, cols, rows := parsePtyPayload(req.Payload)
			mu.Lock()
			if ptmx != nil {
				pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
			}
			mu.Unlock()
			req.Reply(true, nil)

		case "window-change":
			cols, rows := parseWindowChange(req.Payload)
			mu.Lock()
			if ptmx != nil {
				pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
			}
			mu.Unlock()
			if req.WantReply {
				req.Reply(true, nil)
			}

		case "shell", "exec":
			mu.Lock()
			if ptmx != nil {
				mu.Unlock()
				req.Reply(false, nil)
				continue
			}
			var cmd *exec.Cmd
			if req.Type == "exec" {
				cmdStr := parseString(req.Payload)
				cmd = exec.Command("/bin/sh", "-c", cmdStr)
			} else {
				cmd = exec.Command("/bin/bash")
			}
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			var err error
			ptmx, err = pty.Start(cmd)
			mu.Unlock()
			if err != nil {
				req.Reply(false, nil)
				return
			}
			req.Reply(true, nil)
			go io.Copy(ch, ptmx)
			go io.Copy(ptmx, ch)
			cmd.Wait()
			return

		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// parsePtyPayload decodes the pty-req payload per RFC 4254 §6.2.
func parsePtyPayload(b []byte) (term string, cols, rows uint32) {
	cols, rows = 80, 24
	if len(b) < 4 {
		return
	}
	termLen := binary.BigEndian.Uint32(b)
	b = b[4:]
	if uint32(len(b)) < termLen+8 {
		return
	}
	term = string(b[:termLen])
	b = b[termLen:]
	cols = binary.BigEndian.Uint32(b)
	rows = binary.BigEndian.Uint32(b[4:])
	return
}

func parseWindowChange(b []byte) (cols, rows uint32) {
	if len(b) < 8 {
		return 80, 24
	}
	return binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:])
}

func parseString(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	l := binary.BigEndian.Uint32(b)
	b = b[4:]
	if uint32(len(b)) < l {
		return ""
	}
	return string(b[:l])
}
