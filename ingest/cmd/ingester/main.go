// Command ingester reads MeshCore observer traffic (live MQTT, a replay file
// or the synthetic generator), converts it to Parquet rows and writes them to
// S3-compatible storage partitioned by region/date/hour.
//
// Every setting is a flag with an environment-variable default (12-factor).
// Secrets (MQTT_PASSWORD, S3_SECRET_KEY) are env-only so they never show in
// a process listing.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"meshtwin/ingest/internal/ingest"
	"meshtwin/ingest/internal/replay"
	"meshtwin/ingest/internal/source"
	"meshtwin/ingest/internal/synth"
)

type config struct {
	Mode       string
	MQTT       source.MQTTConfig
	ReplayFile string
	Speed      float64
	SynthDur   time.Duration
	SynthRate  float64
	SynthSeed  uint64
	RecordFile string

	S3Endpoint, S3AccessKey, S3SecretKey, S3Bucket string
	S3TLS                                          bool

	FlushRows int
	FlushAge  time.Duration
	HTTPAddr  string
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func envNum[T float64 | int | uint64](k string, def T) T {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		slog.Error("bad numeric env var, using default", "var", k, "value", v)
		return def
	}
	return T(f)
}

func envDur(k string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(env(k, def.String()))
	if err != nil {
		slog.Error("bad duration env var, using default", "var", k)
		return def
	}
	return d
}

func parseConfig() config {
	var c config
	flag.StringVar(&c.Mode, "mode", env("MODE", "live"), "live | replay | synthetic")
	flag.StringVar(&c.MQTT.URL, "mqtt-url", env("MQTT_URL", "tcp://localhost:1883"), "broker URL: tcp://, ssl://, ws://, wss://")
	flag.StringVar(&c.MQTT.Username, "mqtt-username", env("MQTT_USERNAME", ""), "broker username (password: env MQTT_PASSWORD)")
	flag.StringVar(&c.MQTT.Topic, "mqtt-topic", env("MQTT_TOPIC", "meshcore/SEA/+/packets"), "region-filtered topic; never #")
	flag.Float64Var(&c.MQTT.MaxPerSec, "mqtt-max-per-sec", envNum("MQTT_MAX_PER_SEC", 50.0), "drop messages above this rate (0 = no cap)")
	flag.StringVar(&c.ReplayFile, "replay-file", env("REPLAY_FILE", ""), "NDJSON capture for -mode replay")
	flag.Float64Var(&c.Speed, "replay-speed", envNum("REPLAY_SPEED", 0.0), "1 = real time, 60 = 1h/min, 0 = as fast as possible")
	flag.DurationVar(&c.SynthDur, "synth-duration", envDur("SYNTH_DURATION", time.Hour), "simulated time span for -mode synthetic")
	flag.Float64Var(&c.SynthRate, "synth-rate", envNum("SYNTH_RATE", 30.0), "packets/min for -mode synthetic")
	flag.Uint64Var(&c.SynthSeed, "synth-seed", envNum("SYNTH_SEED", uint64(1)), "seed for -mode synthetic")
	flag.StringVar(&c.RecordFile, "record-file", env("RECORD_FILE", ""), "also append every raw message to this NDJSON file (real data: keep private)")
	flag.StringVar(&c.S3Endpoint, "s3-endpoint", env("S3_ENDPOINT", "localhost:9000"), "MinIO/S3 host:port")
	flag.StringVar(&c.S3AccessKey, "s3-access-key", env("S3_ACCESS_KEY", ""), "access key (secret: env S3_SECRET_KEY)")
	flag.StringVar(&c.S3Bucket, "s3-bucket", env("S3_BUCKET", "meshtwin"), "bucket")
	flag.BoolVar(&c.S3TLS, "s3-tls", env("S3_TLS", "false") == "true", "use HTTPS to S3")
	flag.IntVar(&c.FlushRows, "flush-rows", envNum("FLUSH_ROWS", 100_000), "write a partition's file at this many rows")
	flag.DurationVar(&c.FlushAge, "flush-age", envDur("FLUSH_AGE", 5*time.Minute), "or when its oldest buffered row is this old")
	flag.StringVar(&c.HTTPAddr, "http-addr", env("HTTP_ADDR", ":8080"), "metrics and health endpoints")
	flag.Parse()
	c.MQTT.Password = os.Getenv("MQTT_PASSWORD")
	c.S3SecretKey = os.Getenv("S3_SECRET_KEY")
	return c
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(parseConfig()); err != nil {
		slog.Error("ingester failed", "err", err)
		os.Exit(1)
	}
}

func run(c config) error {
	// SIGTERM (Kubernetes) or Ctrl-C: stop reading, drain, flush, exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	src, err := pickSource(c)
	if err != nil {
		return err
	}
	// Storage often starts after us (compose, Kubernetes): retry for a minute
	// before giving up, instead of depending on startup order.
	var store *ingest.S3Store
	for attempt := 1; ; attempt++ {
		store, err = ingest.NewS3Store(ctx, c.S3Endpoint, c.S3AccessKey, c.S3SecretKey, c.S3Bucket, c.S3TLS)
		if err == nil {
			break
		}
		if attempt == 12 || ctx.Err() != nil {
			return fmt.Errorf("object storage: %w", err)
		}
		slog.Warn("object storage not reachable yet, retrying", "endpoint", c.S3Endpoint, "err", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
	}
	w := &ingest.Writer{Store: store, FlushRows: c.FlushRows, FlushAge: c.FlushAge, Now: time.Now}

	var mqttUp, flushOK atomic.Bool
	flushOK.Store(true)
	srv := &http.Server{Addr: c.HTTPAddr, Handler: httpHandler(c.Mode, &mqttUp, &flushOK), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "err", err)
		}
	}()
	defer srv.Close()

	var rec *json.Encoder
	if c.RecordFile != "" {
		f, err := os.OpenFile(c.RecordFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // 0600: real data
		if err != nil {
			return fmt.Errorf("record file: %w", err)
		}
		bw := bufio.NewWriter(f)
		defer func() { _ = bw.Flush(); _ = f.Close() }()
		rec = json.NewEncoder(bw)
	}

	// The source runs in its own goroutine and hands records over a bounded
	// channel: if storage is slow, the channel fills and the source blocks
	// (backpressure) instead of memory growing without limit.
	records := make(chan replay.Record, 10_000)
	srcErr := make(chan error, 1)
	go func() {
		defer close(records)
		srcErr <- src(ctx, func(r replay.Record) error {
			select {
			case records <- r:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, &mqttUp)
	}()

	flush := func(fctx context.Context, force bool) {
		st, err := w.Flush(fctx, force)
		filesWritten.Add(float64(st.Files))
		bytesWritten.Add(float64(st.Bytes))
		if st.Files > 0 {
			slog.Info("flushed", "files", st.Files, "rows", st.Rows, "bytes", st.Bytes)
		}
		flushOK.Store(err == nil)
		if err != nil {
			flushErrors.Inc()
			slog.Error("flush failed; rows stay buffered and will be retried", "err", err)
		}
		buffered.Set(float64(w.Buffered()))
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for running := true; running; {
		select {
		case r, ok := <-records:
			if !ok {
				running = false
				break
			}
			if rec != nil {
				if err := rec.Encode(r); err != nil {
					slog.Error("record file write", "err", err)
				}
			}
			handle(w, r)
		case <-ticker.C:
			flush(ctx, false)
		}
	}

	// Final flush gets its own deadline: ctx is already cancelled on SIGTERM.
	fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	flush(fctx, true)
	if n := w.Buffered(); n > 0 {
		return fmt.Errorf("exiting with %d rows not written", n)
	}
	if err := <-srcErr; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	slog.Info("ingester stopped cleanly")
	return nil
}

func handle(w *ingest.Writer, r replay.Record) {
	res, err := ingest.Convert(r)
	switch {
	case errors.Is(err, ingest.ErrNotPacket):
		messages.WithLabelValues("not_packet").Inc()
		return
	case errors.Is(err, ingest.ErrBadTopic):
		messages.WithLabelValues("bad_topic").Inc()
		return
	case err != nil:
		messages.WithLabelValues("decode_error").Inc()
		slog.Debug("decode error", "topic", r.Topic, "err", err)
		return
	}
	if res.HashMismatch {
		hashMismatches.Inc()
	}
	if !w.Add(res) {
		messages.WithLabelValues("duplicate").Inc()
		return
	}
	messages.WithLabelValues("ok").Inc()
	payloadTypes.WithLabelValues(strconv.Itoa(int(res.Reception.PayloadType))).Inc()
	buffered.Set(float64(w.Buffered()))
}

type sourceFunc func(ctx context.Context, emit func(replay.Record) error, connected *atomic.Bool) error

func pickSource(c config) (sourceFunc, error) {
	switch c.Mode {
	case "live":
		return func(ctx context.Context, emit func(replay.Record) error, up *atomic.Bool) error {
			return source.RunMQTT(ctx, c.MQTT, emit, source.Hooks{
				Connected: func(b bool) { up.Store(b); mqttConnected.Set(map[bool]float64{true: 1}[b]) },
				Dropped:   func(reason string) { messages.WithLabelValues(reason).Inc() },
			})
		}, nil
	case "replay":
		if c.ReplayFile == "" {
			return nil, errors.New("-mode replay needs -replay-file")
		}
		return func(ctx context.Context, emit func(replay.Record) error, _ *atomic.Bool) error {
			f, err := os.Open(c.ReplayFile)
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := replay.Play(ctx, f, c.Speed, emit)
			slog.Info("replay finished", "records", st.Records, "skipped_lines", st.Skipped)
			return err
		}, nil
	case "synthetic":
		return func(ctx context.Context, emit func(replay.Record) error, _ *atomic.Bool) error {
			cfg := synth.DefaultConfig()
			cfg.Seed = c.SynthSeed
			rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed))
			net, err := synth.NewNetwork(cfg, rng)
			if err != nil {
				return err
			}
			// Start the simulation at "now" so partitions look like live data.
			tr := synth.Traffic{Start: time.Now().UTC().Truncate(time.Second), Duration: c.SynthDur, PacketsPerMin: c.SynthRate}
			_, err = net.Generate(cfg, tr, rng, emit)
			return err
		}, nil
	}
	return nil, fmt.Errorf("unknown -mode %q (want live, replay or synthetic)", c.Mode)
}
