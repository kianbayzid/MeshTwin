// Package source holds the live MQTT input. Replay and synthetic inputs live
// in internal/replay and internal/synth; all three emit replay.Record.
package source

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"log/slog"
	"net/url"
	"strings"
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

	// Persistence (DDIL). Default QoS 0 + clean session: nothing is queued
	// while we are down, and public community brokers carry no session state
	// for us; keep that default there out of politeness. For our own broker or
	// observer, QoS 1 + CleanSession false + a stable ClientID makes the broker
	// queue messages while the ingester restarts. With QoS 1 the rate cap
	// throttles instead of dropping: the worker waits, the ack waits with it,
	// and the broker's inflight window slows delivery, so the queued burst
	// after a restart arrives intact.
	//
	// What a QoS 1 ack does NOT cover: a message is acked once it is handed to
	// the ingester, not once it is on disk. On SIGKILL or OOM we lose every
	// acked record not yet flushed: those waiting in main's records channel
	// (up to 10k) and the rows buffered in the Writer (up to FLUSH_AGE /
	// FLUSH_ROWS). Messages still in this package's queue are unacked, so the
	// broker redelivers them with a persistent session (clean session off).
	// SIGTERM drains and flushes everything (to the spool if storage is down)
	// when SPOOL_DIR is set; without a spool and with storage down the final
	// flush fails and the rows are lost (exit "rows not written"). Files
	// already in the spool survive either way.
	//
	// Delivery is at-least-once: acks for an old connection are dropped after
	// a reconnect (paho #726), so a message can be written twice. reception_id
	// is deterministic, and readers dedup on it.
	QoS          byte   // 0 or 1
	ClientID     string // empty = meshtwin-<random>
	CleanSession bool
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
	rc := newReceiver(cfg, emit, h)
	enqueue, workerDone := rc.start(ctx, queueSize)
	onMessage := func(_ mqtt.Client, m mqtt.Message) { enqueue(m) }

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.URL).
		SetClientID(cmp.Or(cfg.ClientID, clientID())).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetCleanSession(cfg.CleanSession).
		// A persistent session can deliver queued messages before Subscribe
		// returns; route those to the same handler (which re-checks the topic).
		SetDefaultPublishHandler(onMessage).
		// We ack ourselves, and not for a message abandoned at shutdown, so
		// the broker redelivers it to a persistent session.
		SetAutoAckDisabled(true).
		// Ordered: paho runs onMessage inline, which only enqueues; the one
		// worker acks in arrival order, as MQTT 3.1.1 [MQTT-4.6.0-2] requires.
		SetOrderMatters(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(5 * time.Minute).
		SetKeepAlive(60 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			// Subscribe on every (re)connect: a clean session forgets subscriptions
			// (and re-subscribing to a persistent session is harmless).
			if t := c.Subscribe(cfg.Topic, cfg.QoS, onMessage); t.Wait() && t.Error() != nil {
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
	<-workerDone // after this nothing calls emit, so the caller may close its channel
	h.Connected(false)
	return nil // shutdown is the only way out; kept as error for a uniform source signature
}

// receiver decides what happens to each message; it is separate from the
// paho client so it can be tested without a broker.
type receiver struct {
	cfg     MQTTConfig
	limiter *rate.Limiter
	emit    func(replay.Record) error
	h       Hooks
}

func newReceiver(cfg MQTTConfig, emit func(replay.Record) error, h Hooks) *receiver {
	limiter := rate.NewLimiter(rate.Inf, 0)
	if cfg.MaxPerSec > 0 {
		limiter = rate.NewLimiter(rate.Limit(cfg.MaxPerSec), int(cfg.MaxPerSec)+1)
	}
	return &receiver{cfg: cfg, limiter: limiter, emit: emit, h: h}
}

// queueSize bounds messages between paho and the worker. If it fills (the
// broker's inflight window is larger than this: a misconfiguration; the
// platform sets mosquitto max_inflight_messages 20, well below it), enqueue
// blocks paho's router. Keepalives may then time out and the connection drop.
// With QoS 1 and a persistent session the broker redelivers the unacked
// messages on reconnect: slow, but nothing is lost. With QoS 0, or QoS 1 with
// a clean session, messages are lost while the router is blocked or
// reconnecting.
const queueSize = 1024

// message is the part of mqtt.Message the worker uses (faked in tests).
type message interface {
	Topic() string
	Payload() []byte
	Ack()
}

// start runs one worker that handles queued messages in arrival order:
// topic check, rate limit, emit, then Ack. It returns the enqueue function
// for paho's handler and a channel closed once the worker has exited, which
// it does when ctx is done. The queue is never closed, so a late paho
// callback cannot panic; it just sees ctx done. Messages abandoned in the
// queue at shutdown are never acked, so a persistent session redelivers them.
func (r *receiver) start(ctx context.Context, size int) (enqueue func(message), done <-chan struct{}) {
	q := make(chan message, size)
	d := make(chan struct{})
	go func() {
		defer close(d)
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-q:
				if ctx.Err() != nil {
					return // both ready: shutdown wins, the message stays unacked
				}
				if r.receive(ctx, m.Topic(), m.Payload()) {
					m.Ack()
				}
			}
		}
	}()
	return func(m message) {
		select {
		case q <- m:
		case <-ctx.Done():
		}
	}, d
}

// receive handles one message and reports whether to ack it: true once it is
// handed to the ingester or deliberately dropped, false when abandoned at
// shutdown (so a QoS 1 session redelivers it).
func (r *receiver) receive(ctx context.Context, topic string, payload []byte) bool {
	switch {
	case !topicMatches(r.cfg.Topic, topic):
		r.h.Dropped("topic_mismatch")
		return true
	case len(payload) > MaxMessageBytes:
		r.h.Dropped("oversize")
		return true
	case r.cfg.QoS == 0 && !r.limiter.Allow():
		r.h.Dropped("rate_limited") // QoS 0 cannot push back, so drop and count
		return true
	}
	if r.cfg.QoS > 0 {
		// Backpressure: wait for a token; the ack (and the broker) waits too.
		if err := r.limiter.Wait(ctx); err != nil {
			r.h.Dropped("shutdown")
			return false
		}
	}
	rec := replay.Record{RecvTime: time.Now().UTC(), Topic: topic, Payload: append([]byte(nil), payload...)}
	if err := r.emit(rec); err != nil {
		r.h.Dropped("shutdown")
		return false
	}
	return true
}

// topicMatches applies MQTT filter rules: "+" matches one level, a trailing
// "#" matches the rest (including none), and a filter starting with a
// wildcard does not match "$" topics like $SYS.
func topicMatches(filter, topic string) bool {
	if strings.HasPrefix(topic, "$") && (strings.HasPrefix(filter, "+") || strings.HasPrefix(filter, "#")) {
		return false
	}
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, level := range f {
		if level == "#" {
			return i == len(f)-1
		}
		if i >= len(t) || (level != "+" && level != t[i]) {
			return false
		}
	}
	return len(f) == len(t)
}

// clientID is meshtwin-<random>, so broker operators can tell who we are.
func clientID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "meshtwin-" + hex.EncodeToString(b)
}
