package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	readHeaderTimeout   = 10 * time.Second
	sessionIdealTimeout = 30 * time.Minute
	shutdownTimeout     = 5 * time.Second
)

func serveHttp(ctx context.Context, s *mcp.Server, addr, token string) error {
	if token == "" {
		return errors.New("SAFEDB_TOKEN must be set when SAFEDB_HTTP_ADDR is")
	}
	hs := &http.Server{Addr: addr, Handler: httpHandler(s, token), ReadHeaderTimeout: readHeaderTimeout}

	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	slog.Info("listening", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return hs.Shutdown(ctx)
}

func httpHandler(s *mcp.Server, token string) http.Handler {
	h := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			return s
		},
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionIdealTimeout},
	)
	verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{}, nil
	}
	return auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)

}
