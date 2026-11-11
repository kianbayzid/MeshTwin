package source

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshtwin/ingest/internal/replay"
)

func TestTopicMatches(t *testing.T) {
	tests := []struct {
		filter, topic string
		want          bool
	}{
		{"meshcore/SEA/+/packets", "meshcore/SEA/abc/packets", true},
		{"meshcore/SEA/+/packets", "meshcore/PDX/abc/packets", false},
		{"meshcore/SEA/+/packets", "meshcore/SEA/abc/status", false},
		{"meshcore/SEA/+/packets", "meshcore/SEA/packets", false},
		{"meshcore/SEA/+/packets", "meshcore/SEA/a/b/packets", false},
		{"meshcore/SEA/#", "meshcore/SEA/abc/packets", true},
		{"meshcore/SEA/#", "meshcore/SEA", true}, // # also matches the parent level
		{"meshcore/SEA/#", "meshcore/PDX/abc", false},
		{"meshcore/#/x", "meshcore/a/x", false}, // # only valid last
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},
		{"+/x", "$SYS/x", false},
		{"#", "$SYS/broker", false},
		{"$SYS/#", "$SYS/broker", true},
	}
	for _, tc := range tests {
		if got := topicMatches(tc.filter, tc.topic); got != tc.want {
			t.Errorf("topicMatches(%q, %q) = %v, want %v", tc.filter, tc.topic, got, tc.want)
		}
	}
}

type counter struct {
	mu      sync.Mutex
	emitted int
	dropped map[string]int
}

func (c *counter) emit(replay.Record) error { c.mu.Lock(); c.emitted++; c.mu.Unlock(); return nil }
func (c *counter) drop(reason string)       { c.mu.Lock(); c.dropped[reason]++; c.mu.Unlock() }

// A burst of 25 messages at a 20/s cap (burst 21): QoS 0 drops the 4 extra
// and acks them; QoS 1 delivers all 25 and waits ~4/20 s for the tokens,
// which is the backpressure the broker sees as delayed acks.
func TestReceiverRateLimitByQoS(t *testing.T) {
	tests := []struct {
		qos         byte
		wantEmitted int
		wantDropped int
		minElapsed  time.Duration
	}{
		{0, 21, 4, 0},
		{1, 25, 0, 150 * time.Millisecond},
	}
	for _, tc := range tests {
		c := &counter{dropped: map[string]int{}}
		rc := newReceiver(MQTTConfig{Topic: "meshcore/SEA/+/packets", MaxPerSec: 20, QoS: tc.qos}, c.emit, Hooks{Dropped: c.drop})
		start := time.Now()
		for range 25 {
			if !rc.receive(context.Background(), "meshcore/SEA/obs/packets", []byte("{}")) {
				t.Fatalf("qos %d: message not acked", tc.qos)
			}
		}
		elapsed := time.Since(start)
		if c.emitted != tc.wantEmitted || c.dropped["rate_limited"] != tc.wantDropped {
			t.Errorf("qos %d: emitted %d, rate_limited %d; want %d, %d", tc.qos, c.emitted, c.dropped["rate_limited"], tc.wantEmitted, tc.wantDropped)
		}
		if elapsed < tc.minElapsed {
			t.Errorf("qos %d: took %v, want >= %v (throttled, not dropped)", tc.qos, elapsed, tc.minElapsed)
		}
	}
}

// At shutdown a QoS 1 handler waiting for a token returns promptly and does
// not ack, so the broker redelivers the message to the persistent session.
func TestReceiverShutdownUnblocksWithoutAck(t *testing.T) {
	c := &counter{dropped: map[string]int{}}
	rc := newReceiver(MQTTConfig{Topic: "t/+", MaxPerSec: 1, QoS: 1}, c.emit, Hooks{Dropped: c.drop})
	ctx, cancel := context.WithCancel(context.Background())
	for range 2 { // use up the burst of 2
		rc.receive(ctx, "t/a", []byte("{}"))
	}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	if rc.receive(ctx, "t/a", []byte("{}")) {
		t.Error("message abandoned at shutdown was acked")
	}
	if time.Since(start) > 500*time.Millisecond || c.dropped["shutdown"] != 1 {
		t.Errorf("shutdown took %v, shutdown drops %d; want prompt, 1", time.Since(start), c.dropped["shutdown"])
	}
}

// Messages from a persistent session's default handler are filtered too.
func TestReceiverDropsOtherTopics(t *testing.T) {
	c := &counter{dropped: map[string]int{}}
	rc := newReceiver(MQTTConfig{Topic: "meshcore/SEA/+/packets"}, c.emit, Hooks{Dropped: c.drop})
	if !rc.receive(context.Background(), "meshcore/PDX/obs/packets", []byte("{}")) {
		t.Error("dropped message not acked")
	}
	if c.emitted != 0 || c.dropped["topic_mismatch"] != 1 {
		t.Errorf("emitted %d, topic_mismatch %d; want 0, 1", c.emitted, c.dropped["topic_mismatch"])
	}
}

// fakeMsg records its ack into a shared, ordered log.
type fakeMsg struct {
	id    int
	topic string
	log   *ackLog
}

type ackLog struct {
	mu  sync.Mutex
	ids []int
}

func (m fakeMsg) Topic() string   { return m.topic }
func (m fakeMsg) Payload() []byte { return []byte("{}") }
func (m fakeMsg) Ack()            { m.log.mu.Lock(); m.log.ids = append(m.log.ids, m.id); m.log.mu.Unlock() }

func (l *ackLog) snapshot() []int { l.mu.Lock(); defer l.mu.Unlock(); return slices.Clone(l.ids) }

// PUBACKs must leave in arrival order [MQTT-4.6.0-2], even when the limiter
// and emit take varying time per message and some messages are dropped.
func TestReceiverAcksInArrivalOrder(t *testing.T) {
	for _, qos := range []byte{0, 1} {
		log := &ackLog{}
		emit := func(r replay.Record) error {
			time.Sleep(time.Duration(len(r.Topic)%3) * time.Millisecond) // varied work
			return nil
		}
		rc := newReceiver(MQTTConfig{Topic: "t/+", MaxPerSec: 500, QoS: qos}, emit, Hooks{Dropped: func(string) {}})
		ctx, cancel := context.WithCancel(context.Background())
		enqueue, done := rc.start(ctx, 8) // smaller than the burst: enqueue blocks sometimes
		const n = 600                     // more than the 501 burst: QoS 1 waits on the limiter
		for i := range n {
			topic := "t/" + strings.Repeat("x", i%5)
			if i%7 == 0 {
				topic = "other/topic" // dropped by the filter, still acked in order
			}
			enqueue(fakeMsg{id: i, topic: topic, log: log})
		}
		deadline := time.Now().Add(10 * time.Second)
		for len(log.snapshot()) < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
		<-done
		got := log.snapshot()
		if len(got) != n || !slices.IsSorted(got) {
			t.Errorf("qos %d: %d acks, sorted=%v; want %d in arrival order", qos, len(got), slices.IsSorted(got), n)
		}
	}
}

// Shutdown: the worker stops promptly even while emit is blocked, and never
// acks a message it did not hand over (the broker redelivers those).
func TestReceiverShutdownStopsWorkerWithoutAcks(t *testing.T) {
	log := &ackLog{}
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan struct{})
	emit := func(replay.Record) error { // like main's emit with a full channel
		close(blocked)
		<-ctx.Done()
		return ctx.Err()
	}
	rc := newReceiver(MQTTConfig{Topic: "t/+", QoS: 1}, emit, Hooks{Dropped: func(string) {}})
	enqueue, done := rc.start(ctx, 4)
	for i := range 4 {
		enqueue(fakeMsg{id: i, topic: "t/a", log: log})
	}
	<-blocked
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancel")
	}
	enqueue(fakeMsg{id: 99, topic: "t/a", log: log}) // a late paho callback: must not block or panic
	if got := log.snapshot(); len(got) != 0 {
		t.Errorf("acked %v at shutdown, want none", got)
	}
}

// Models main: emit sends on records; once RunMQTT's wait for the worker
// returns, records is closed. Concurrent paho callbacks around the cancel
// must never cause a send on the closed channel (was 36 panics in 200).
func TestReceiverNoSendAfterClose(t *testing.T) {
	var panics atomic.Int64
	for range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		records := make(chan replay.Record, 10_000)
		emit := func(r replay.Record) error {
			select {
			case records <- r:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		rc := newReceiver(MQTTConfig{Topic: "t/+", QoS: 1}, emit, Hooks{Dropped: func(string) {}})
		enqueue, done := rc.start(ctx, queueSize)
		var wg sync.WaitGroup
		for i := range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if recover() != nil {
						panics.Add(1)
					}
				}()
				enqueue(fakeMsg{id: i, topic: "t/a", log: &ackLog{}})
			}()
		}
		cancel()
		<-done // what RunMQTT waits for before returning
		func() {
			defer func() {
				if recover() != nil {
					panics.Add(1)
				}
			}()
			close(records)
		}()
		wg.Wait()
	}
	if n := panics.Load(); n != 0 {
		t.Errorf("%d panics in 200 trials, want 0", n)
	}
}
