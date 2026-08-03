package orchestrator

import (
	"errors"
	"testing"
)

func TestIsSessionExhausted(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rakp exhaustion", errors.New("rakp status code error: (0x01) Insufficient resources to create a session"), true},
		{"lowercase resource singular", errors.New("insufficient resource"), true},
		{"auth failure", errors.New("RAKP2 HMAC is invalid"), false},
		{"unreachable", errors.New("dial udp: i/o timeout"), false},
	}
	for _, c := range cases {
		if got := isSessionExhausted(c.err); got != c.want {
			t.Errorf("%s: isSessionExhausted(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
