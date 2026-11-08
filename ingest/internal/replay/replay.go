// Package replay reads and plays back recorded MQTT traffic stored as NDJSON:
// one Record per line. The synthetic generator writes the same format, so
// live, replay and synthetic modes all feed the ingester identical input.
package replay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Record is one MQTT message as received.
type Record struct {
	RecvTime time.Time       `json:"recv_time"` // when the recorder (or generator) received it
	Topic    string          `json:"topic"`
	Payload  json.RawMessage `json:"payload"` // message body, byte-for-byte as published
}

// Stats counts what Play saw.
type Stats struct {
	Records int // emitted
	Skipped int // malformed lines, skipped so one bad line can't stop a long replay
}

// Play reads records from r and calls emit for each in file order. With
// speed > 0 it waits the recorded gap between records divided by speed
// (1 = real time, 60 = an hour per minute). With speed <= 0 it does not wait.
func Play(ctx context.Context, r io.Reader, speed float64, emit func(Record) error) (Stats, error) {
	return play(ctx, r, speed, emit, sleepCtx)
}

func play(ctx context.Context, r io.Reader, speed float64, emit func(Record) error,
	sleep func(context.Context, time.Duration) error) (Stats, error) {
	var st Stats
	var prev time.Time
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20) // observer messages are ~1 KB; 1 MB leaves room
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil || rec.Topic == "" || len(rec.Payload) == 0 {
			st.Skipped++
			continue
		}
		// Out-of-order or missing timestamps play immediately rather than stall.
		if speed > 0 && !prev.IsZero() && rec.RecvTime.After(prev) {
			if err := sleep(ctx, time.Duration(float64(rec.RecvTime.Sub(prev))/speed)); err != nil {
				return st, err
			}
		}
		if !rec.RecvTime.IsZero() {
			prev = rec.RecvTime
		}
		if err := emit(rec); err != nil {
			return st, fmt.Errorf("record %d: %w", st.Records+st.Skipped+1, err)
		}
		st.Records++
	}
	return st, sc.Err()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
