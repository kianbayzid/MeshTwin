// Package source holds the live MQTT input. Replay and synthetic inputs live
// in internal/replay and internal/synth; all three emit replay.Record.
package source

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"log/slog"
	"net/url"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"golang.org/x/time/rate"

	"meshtwin/ingest/internal/replay"
)

// MaxMessageBytes drops anything larger: observer messages are ~1 KB and a
// MeshCore frame is at most 255 bytes, so a huge message is junk or abuse.
const MaxMessageBytes = 16 * 1024

// MQTTConfig describes one broker subscription.
type MQTTConfig struct {
	URL       string  // tcp://host:1883, ssl://host:8883, wss://host:443/
	Username  string  // optional
	Password  string  // optional; from a Secret, never a flag
	Topic     string  // e.g. meshcore/SEA/+/packets; never "#"
	MaxPerSec float64 // per-source message cap; 0 = unlimited
}

// Hooks lets the caller observe the connection without this package
// depending on Prometheus.
type Hooks struct {
	Connected func(bool)
	Dropped   func(reason string)
}

// RunMQTT subscribes (read-only: it never publishes) and calls emit for each
// message until ctx is done. Connection failures are logged and retried,
// never returned. Reconnects with exponential backoff, 1 s
// doubling to a 5 min cap, so a broker outage is not met with a retry storm.
func RunMQTT(ctx context.Context, cfg MQTTConfig, emit func(replay.Record) error, h Hooks) error {
	limiter := rate.NewLimiter(rate.Inf, 0)
	if cfg.MaxPerSec > 0 {
		limiter = rate.NewLimiter(rate.Limit(cfg.MaxPerSec), int(cfg.MaxPerSec)+1)
	}
	onMessage := func(_ mqtt.Client, m mqtt.Message) {
		switch {
		case len(m.Payload()) > MaxMessageBytes:
			h.Dropped("oversize")
		case !limiter.Allow():
			h.Dropped("rate_limited")
		default:
			rec := replay.Record{RecvTime: time.Now().UTC(), Topic: m.Topic(), Payload: append([]byte(nil), m.Payload()...)}
			if err := emit(rec); err != nil {
				h.Dropped("shutdown")
			}
		}
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.URL).
		SetClientID(clientID()).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(5 * time.Minute).
		SetKeepAlive(60 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			// Subscribe on every (re)connect: a clean session forgets subscriptions.
			if t := c.Subscribe(cfg.Topic, 0, onMessage); t.Wait() && t.Error() != nil {
				slog.Error("subscribe failed", "topic", cfg.Topic, "err", t.Error())
				return
			}
			slog.Info("mqtt connected", "broker", cfg.URL, "topic", cfg.Topic)
			h.Connected(true)
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			slog.Warn("mqtt connection lost", "err", err)
			h.Connected(false)
		})

	opts.SetConnectionAttemptHandler(func(broker *url.URL, tc *tls.Config) *tls.Config {
		slog.Info("mqtt connecting", "broker", broker.Redacted())
		return tc
	})

	// With ConnectRetry the token only completes once connected, so don't wait
	// on it: paho keeps retrying in the background and ctx stays in charge.
	client := mqtt.NewClient(opts)
	client.Connect()
	<-ctx.Done()
	client.Disconnect(500)
	h.Connected(false)
	return nil // shutdown is the only way out; kept as error for a uniform source signature
}

// clientID is meshtwin-<random>, so broker operators can tell who we are.
func clientID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "meshtwin-" + hex.EncodeToString(b)
}
