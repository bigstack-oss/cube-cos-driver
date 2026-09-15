package clusterssh

import (
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// The exact shape seen on the lab cluster: two multi-minute install steps ended
// with "wait: remote command exited without exit status or exit signal" and no
// output, while the identical command run on the node succeeded. That is a
// channel that closed carrying no exit status -- a dead transport, not a
// command that failed.
func TestTransportLossIsNotACommandFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"missing exit status", &ssh.ExitMissingError{}, true},
		{"missing exit status, wrapped", fmt.Errorf("wait: %w", &ssh.ExitMissingError{}), true},
		{"eof", io.EOF, true},
		{"closed connection", net.ErrClosed, true},
		// A command that ran and exited non-zero is a command failure, and must
		// keep reading as one -- misclassifying it would tell an operator to go
		// look on the cluster for work that genuinely failed here.
		{"non-zero exit", &ssh.ExitError{}, false},
		{"plain error", errors.New("something else"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isTransportLoss(c.err); got != c.want {
			t.Errorf("%s: isTransportLoss = %v, want %v", c.name, got, c.want)
		}
	}
}

// Callers match on the sentinel, so it has to survive the wrapping Run does.
func TestConnectionLostIsMatchable(t *testing.T) {
	err := fmt.Errorf("%s: %w", "bash /tmp/install.sh", ErrConnectionLost)
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("errors.Is(%v, ErrConnectionLost) = false", err)
	}
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatal("sentinel did not survive wrapping")
	}
}
