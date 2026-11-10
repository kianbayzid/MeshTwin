// Command api serves the analytics summaries as a read-only REST API plus
// the Leaflet map page (docs/api-contract.md).
//
// Every setting is a flag with an environment-variable default (12-factor);
// the S3 secret key is env-only so it never shows in a process listing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"meshtwin/api/internal/server"
	"meshtwin/api/internal/store"
)

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

type config struct {
	Listen, SummariesDir, StaticDir                string
	S3Endpoint, S3AccessKey, S3SecretKey, S3Bucket string
	S3TLS                                          bool
	CacheTTL                                       time.Duration
}

func parseConfig() config {
	var c config
	flag.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "API, map page, /metrics and health endpoints")
	flag.StringVar(&c.SummariesDir, "summaries-dir", env("SUMMARIES_DIR", ""), "read summaries from this directory instead of S3 (holds summaries/region=*/)")
	flag.StringVar(&c.StaticDir, "static-dir", env("STATIC_DIR", ""), "serve the map page from this directory instead of the embedded copy (dev)")
	flag.StringVar(&c.S3Endpoint, "s3-endpoint", env("S3_ENDPOINT", "localhost:9000"), "MinIO/S3 host:port")
	flag.StringVar(&c.S3AccessKey, "s3-access-key", env("S3_ACCESS_KEY", ""), "access key (secret: env S3_SECRET_KEY)")
	flag.StringVar(&c.S3Bucket, "s3-bucket", env("S3_BUCKET", "meshtwin"), "bucket")
	flag.BoolVar(&c.S3TLS, "s3-tls", env("S3_TLS", "false") == "true", "use HTTPS to S3")
	ttl, err := time.ParseDuration(env("CACHE_TTL", "30s"))
	if err != nil {
		slog.Error("bad CACHE_TTL, using 30s")
		ttl = 30 * time.Second
	}
	flag.DurationVar(&c.CacheTTL, "cache-ttl", ttl, "how long a loaded summary is reused before re-reading storage")
	flag.Parse()
	c.S3SecretKey = os.Getenv("S3_SECRET_KEY")
	return c
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(parseConfig()); err != nil {
		slog.Error("api failed", "err", err)
		os.Exit(1)
	}
}

func run(c config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st store.Store = store.Local{Root: c.SummariesDir}
	if c.SummariesDir == "" {
		s3, err := store.NewS3(c.S3Endpoint, c.S3AccessKey, c.S3SecretKey, c.S3Bucket, c.S3TLS)
		if err != nil {
			return err
		}
		st = s3
	}
	static := server.Embedded
	if c.StaticDir != "" {
		static = os.DirFS(c.StaticDir)
	}

	srv := &http.Server{
		Addr:              c.Listen,
		Handler:           server.New(st, static, c.CacheTTL).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("api listening", "addr", c.Listen, "local", c.SummariesDir != "", "static_override", c.StaticDir != "")

	select {
	case err := <-errc:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	// SIGTERM: stop accepting, let in-flight requests finish (they are short).
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	slog.Info("api stopped cleanly")
	return nil
}
