// Command api serves the analytics summaries as a read-only REST API plus
// the console (index.html, assets/, optional PMTiles basemaps; docs/api-contract.md).
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
	Listen, SummariesDir, StaticDir, BasemapDir    string
	PrometheusURL                                  string
	S3Endpoint, S3AccessKey, S3SecretKey, S3Bucket string
	S3TLS                                          bool
	CacheTTL                                       time.Duration
}

func parseConfig() config {
	var c config
	flag.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "API, console, /metrics and health endpoints")
	flag.StringVar(&c.SummariesDir, "summaries-dir", env("SUMMARIES_DIR", ""), "read summaries from this directory instead of S3 (holds summaries/region=*/)")
	flag.StringVar(&c.StaticDir, "static-dir", env("STATIC_DIR", ""), "serve the console (index.html, assets/) from this directory instead of the embedded copy (dev, e2e)")
	flag.StringVar(&c.BasemapDir, "basemap-dir", env("BASEMAP_DIR", ""), "serve /basemap/{name}.pmtiles from this directory (empty: off, 404)")
	flag.StringVar(&c.S3Endpoint, "s3-endpoint", env("S3_ENDPOINT", "localhost:9000"), "MinIO/S3 host:port")
	flag.StringVar(&c.S3AccessKey, "s3-access-key", env("S3_ACCESS_KEY", ""), "access key (secret: env S3_SECRET_KEY)")
	flag.StringVar(&c.S3Bucket, "s3-bucket", env("S3_BUCKET", "meshtwin"), "bucket")
	flag.StringVar(&c.PrometheusURL, "prometheus-url", env("PROMETHEUS_URL", ""), "Prometheus base URL for GET /api/v1/status (empty: that endpoint answers 503)")
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
		root, err := os.OpenRoot(c.StaticDir) // like the embedded copy, nothing outside the directory
		if err != nil {
			return fmt.Errorf("static dir: %w", err)
		}
		static = root.FS()
	}

	api := server.New(st, static, c.CacheTTL)
	if err := api.SetPrometheus(c.PrometheusURL); err != nil {
		return err
	}
	if err := api.SetBasemapDir(c.BasemapDir); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              c.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("api listening", "addr", c.Listen, "local", c.SummariesDir != "", "static_override", c.StaticDir != "", "basemap", c.BasemapDir != "", "status_enabled", c.PrometheusURL != "")

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
