// Package clusterssh provides an SSH/scp client for talking to a running
// CubeCOS cluster (VIP), plus a MockClient for unit-testing callers without
// a real host.
package clusterssh

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrConnectionLost means the transport went away while a command was running:
// the channel closed carrying no exit status, so the command's fate is unknown.
// It is deliberately not "the command failed" -- the command may have finished,
// and may still be running on the node. Callers that report a step as failed
// must say which of the two happened, or an operator is told work failed that
// is in fact still going.
var ErrConnectionLost = errors.New("connection lost while the command was running")

// keepaliveInterval is how often an open connection is probed. The cluster's
// sshd runs with ClientAliveInterval 0, so nothing on that side probes either:
// a command that prints nothing for minutes (a helm install, an image import)
// leaves the connection idle, and anything between the driver and the cluster
// that drops idle flows takes it without either end noticing until the channel
// closes with no exit status.
const keepaliveInterval = 30 * time.Second

// Client runs commands and pushes files to a remote cluster node.
type Client interface {
	// Run executes cmd on the remote host, calling onLine for each stdout line
	// (onLine may be nil). Returns an error containing captured stderr on non-zero exit.
	Run(ctx context.Context, cmd string, onLine func(string)) error
	// Push copies a local file to remotePath on the remote host (scp-over-SSH).
	Push(ctx context.Context, localPath, remotePath string) error
	Close() error
}

type sshClient struct {
	conn *ssh.Client
	// done stops the keepalive loop; closed once, by Close.
	done     chan struct{}
	closeOne sync.Once
}

// NewSSHClient dials root@host:22 with password auth (InsecureIgnoreHostKey —
// the VIP is on a trusted mgmt LAN, matching the deploy verifier's ping-only
// trust of the same network).
func NewSSHClient(host, user, password string) (Client, error) {
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	conn, err := ssh.Dial("tcp", host+":22", cfg)
	if err != nil {
		return nil, fmt.Errorf("clusterssh: dial %s: %w", host, err)
	}
	c := &sshClient{conn: conn, done: make(chan struct{})}
	go c.keepalive()
	return c, nil
}

// keepalive probes the connection until Close, so a dead transport is noticed
// as one rather than surfacing later as a command with no exit status.
func (c *sshClient) keepalive() {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			// The request type is the one OpenSSH answers; a failure means the
			// connection is already gone, so there is nothing left to probe.
			if _, _, err := c.conn.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}
}

// isTransportLoss distinguishes "the transport died" from "the command failed".
// ssh.ExitMissingError is the exact shape seen on the lab cluster: the channel
// closed with neither an exit status nor an exit signal.
func isTransportLoss(err error) bool {
	if err == nil {
		return false
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

func (c *sshClient) Run(ctx context.Context, cmd string, onLine func(string)) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("clusterssh: new session: %w", err)
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("clusterssh: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("clusterssh: start %q: %w", cmd, err)
	}

	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if onLine != nil {
				onLine(scanner.Text())
			}
		}
		waitErr := session.Wait()
		// Surface a genuine stdout read error rather than treating it as clean EOF.
		if scanErr := scanner.Err(); scanErr != nil {
			if waitErr != nil {
				waitErr = fmt.Errorf("%w; stdout scan: %s", waitErr, scanErr)
			} else {
				waitErr = fmt.Errorf("stdout scan: %w", scanErr)
			}
		}
		done <- waitErr
	}()

	select {
	case <-ctx.Done():
		session.Close()
		return ctx.Err()
	case err := <-done:
		if err != nil {
			if isTransportLoss(err) {
				return fmt.Errorf("%s: %w", cmd, ErrConnectionLost)
			}
			return fmt.Errorf("%s: %w (%s)", cmd, err, stderr.String())
		}
		return nil
	}
}

// readAck reads one scp sink ack byte: 0 is OK, 1/2 carry an error line.
func readAck(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	if b == 0 {
		return nil
	}
	msg, _ := r.ReadString('\n')
	return fmt.Errorf("scp: %s", strings.TrimSpace(msg))
}

// Push copies localPath to remotePath using the scp sink protocol
// (`scp -t <remotePath>`) over an SSH session — avoids adding an sftp
// dependency for a single-file copy.
func (c *sshClient) Push(ctx context.Context, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("clusterssh: open %s: %w", localPath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("clusterssh: stat %s: %w", localPath, err)
	}

	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("clusterssh: new session: %w", err)
	}
	defer session.Close()

	w, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("clusterssh: stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("clusterssh: stdout pipe: %w", err)
	}
	r := bufio.NewReader(stdout)
	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start(fmt.Sprintf("scp -t %s", remotePath)); err != nil {
		return fmt.Errorf("clusterssh: start scp sink: %w", err)
	}

	// Sink protocol: control line, ack, file bytes, trailing NUL, ack.
	// Fail fast on a bad ack rather than streaming the whole file first.
	errCh := make(chan error, 1)
	go func() {
		defer w.Close()
		fmt.Fprintf(w, "C%04o %d %s\n", info.Mode().Perm(), info.Size(), filepath.Base(localPath))
		if err := readAck(r); err != nil {
			errCh <- err
			return
		}
		if _, err := io.Copy(w, f); err != nil {
			errCh <- err
			return
		}
		fmt.Fprint(w, "\x00")
		errCh <- readAck(r)
	}()

	select {
	case <-ctx.Done():
		session.Close()
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("clusterssh: send %s: %w", localPath, err)
		}
	}

	if err := session.Wait(); err != nil {
		return fmt.Errorf("clusterssh: scp %s -> %s: %w (%s)", localPath, remotePath, err, stderr.String())
	}
	return nil
}

func (c *sshClient) Close() error {
	c.closeOne.Do(func() { close(c.done) })
	return c.conn.Close()
}
