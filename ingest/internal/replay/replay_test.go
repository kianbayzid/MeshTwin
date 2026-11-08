package replay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const sample = `{"recv_time":"2026-11-04T12:00:00Z","topic":"t/a","payload":{"n":1}}
not json
{"recv_time":"2026-11-04T12:00:10Z","topic":"t/b","payload":{"n":2}}

{"recv_time":"2026-11-04T12:00:05Z","topic":"t/c","payload":{"n":3}}
{"recv_time":"2026-11-04T12:00:15Z","topic":"","payload":{"n":4}}
{"recv_time":"2026-11-04T12:00:20Z","topic":"t/e","payload":{"n":5}}
`

func TestPlayOrderSkipsAndDelays(t *testing.T) {
	var topics []string
	var sleeps []time.Duration
	fakeSleep := func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }

	st, err := play(context.Background(), strings.NewReader(sample), 2,
		func(r Record) error { topics = append(topics, r.Topic); return nil }, fakeSleep)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(topics, ","); got != "t/a,t/b,t/c,t/e" {
		t.Errorf("topics = %s", got)
	}
	if st.Records != 4 || st.Skipped != 2 {
		t.Errorf("stats = %+v, want 4 records, 2 skipped", st)
	}
	// Gaps at speed 2: a->b 10s => 5s; b->c goes back in time => none; c->e 15s => 7.5s.
	want := []time.Duration{5 * time.Second, 7500 * time.Millisecond}
	if len(sleeps) != len(want) || sleeps[0] != want[0] || sleeps[1] != want[1] {
		t.Errorf("sleeps = %v, want %v", sleeps, want)
	}
}

func TestPlayNoDelayAtSpeedZero(t *testing.T) {
	noSleep := func(context.Context, time.Duration) error { t.Fatal("slept at speed 0"); return nil }
	if _, err := play(context.Background(), strings.NewReader(sample), 0,
		func(Record) error { return nil }, noSleep); err != nil {
		t.Fatal(err)
	}
}

func TestPlayStopsOnEmitErrorAndCancel(t *testing.T) {
	boom := errors.New("boom")
	_, err := Play(context.Background(), strings.NewReader(sample), 0, func(Record) error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Play(ctx, strings.NewReader(sample), 1, func(Record) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
