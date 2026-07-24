package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSPAServing(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/", "/clusters/deadbeef0000"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		if !strings.Contains(string(body), "<title>") {
			t.Fatalf("GET %s: no html title in %q", path, string(body)[:min(80, len(body))])
		}
	}
}
