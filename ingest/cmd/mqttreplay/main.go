// Command mqttreplay is a TEST TOOL for the chaos demo: it reads an observer
// NDJSON capture (the replay format) and publishes each record's payload to
// its own topic on a broker, so the ingester can be exercised end to end in
// live mode. It only replays files and is never used in real runs; point it
// at your own test broker, never at a community broker.
//
//	MQTT_USERNAME=u MQTT_PASSWORD=p mqttreplay -url tcp://localhost:1883 -file capture.ndjson -speed 60
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"meshtwin/ingest/internal/replay"
)

func main() {
	file := flag.String("file", "", "observer NDJSON capture to publish (required)")
	url := flag.String("url", "tcp://localhost:1883", "test broker URL")
	speed := flag.Float64("speed", 0, "1 = real time, 60 = 1h/min, 0 = as fast as possible")
	qos := flag.Int("qos", 0, "publish QoS, 0 or 1")
	flag.Parse()
	if err := run(*file, *url, *speed, *qos); err != nil {
		slog.Error("mqttreplay failed", "err", err)
		os.Exit(1)
	}
}

func run(file, url string, speed float64, qos int) error {
	if file == "" {
		return errors.New("-file is required")
	}
	if qos != 0 && qos != 1 {
		return fmt.Errorf("-qos must be 0 or 1, got %d", qos)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	client := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(url).
		SetClientID(fmt.Sprintf("meshtwin-mqttreplay-%d", os.Getpid())).
		SetUsername(os.Getenv("MQTT_USERNAME")).
		SetPassword(os.Getenv("MQTT_PASSWORD")).
		SetAutoReconnect(true))
	t := client.Connect()
	if !t.WaitTimeout(30 * time.Second) {
		return fmt.Errorf("connect %s: timeout", url)
	}
	if err := t.Error(); err != nil {
		return fmt.Errorf("connect %s: %w", url, err)
	}
	defer client.Disconnect(1000)

	st, err := replay.Play(ctx, f, speed, func(r replay.Record) error {
		t := client.Publish(r.Topic, byte(qos), false, []byte(r.Payload))
		if !t.WaitTimeout(30 * time.Second) {
			return fmt.Errorf("publish %s: timeout", r.Topic)
		}
		return t.Error()
	})
	slog.Info("published", "records", st.Records, "skipped_lines", st.Skipped)
	return err
}
