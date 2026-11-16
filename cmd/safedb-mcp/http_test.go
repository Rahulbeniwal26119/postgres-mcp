package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHttpAuth(t *testing.T) {
	const token = "test-token"
	ts := httptest.NewServer(httpHandler(newServer(nil), token))
	t.Cleanup(ts.Close)

	for _, header := range []string{"", "Bearer wrong-token", "Basic " + token, token} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", header, resp.StatusCode)
		}
	}
	client := &http.Client{Transport: bearerTransport{token}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: client},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	if res, err := cs.ListTools(t.Context(), nil); err != nil || len(res.Tools) != 3 {
		t.Fatalf("ListTools = %v, %v; want the 3 tools", res, err)
	}

}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
