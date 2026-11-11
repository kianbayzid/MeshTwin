// Command meshgen writes synthetic MeshCore observer traffic as replay NDJSON,
// plus a ground-truth JSON of the simulated network.
//
//	meshgen -duration 1h -rate 30 -out sample.ndjson -truth truth.json
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"time"

	"meshtwin/ingest/internal/replay"
	"meshtwin/ingest/internal/synth"
)

func main() {
	cfg := synth.DefaultConfig()
	flag.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "random seed; same seed => identical output")
	flag.IntVar(&cfg.Repeaters, "repeaters", cfg.Repeaters, "number of repeaters")
	flag.IntVar(&cfg.Companions, "companions", cfg.Companions, "number of companion (chat) nodes")
	flag.IntVar(&cfg.Observers, "observers", cfg.Observers, "number of observers (chosen from repeaters first)")
	flag.IntVar(&cfg.PathHashSize, "hash-size", cfg.PathHashSize, "path hash bytes: 1 (legacy, collides), 2 or 3")
	start := flag.String("start", "2026-11-01T00:00:00Z", "simulation start time (RFC 3339)")
	duration := flag.Duration("duration", time.Hour, "simulated time span")
	rate := flag.Float64("rate", 30, "originated packets per minute, network-wide")
	out := flag.String("out", "-", "NDJSON output path, - for stdout")
	truthPath := flag.String("truth", "", "write ground-truth JSON here (optional)")
	flag.Parse()

	t0, err := time.Parse(time.RFC3339, *start)
	if err != nil {
		log.Fatalf("bad -start: %v", err)
	}
	if err := run(cfg, synth.Traffic{Start: t0, Duration: *duration, PacketsPerMin: *rate}, *out, *truthPath); err != nil {
		log.Fatal(err)
	}
}

func run(cfg synth.Config, tr synth.Traffic, out, truthPath string) error {
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed)) //nolint:gosec // G404: synthetic data must be reproducible from a seed, not secret
	net, err := synth.NewNetwork(cfg, rng)
	if err != nil {
		return err
	}

	var w io.Writer = os.Stdout
	var f *os.File
	if out != "-" {
		if f, err = os.Create(out); err != nil { //nolint:gosec // G304: the operator names the output file on the command line
			return err
		}
		defer func() { _ = f.Close() }() // no-op after the checked Close below; covers early returns
		w = f
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	st, err := net.Generate(cfg, tr, rng, func(r replay.Record) error { return enc.Encode(r) })
	if err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if f != nil {
		if err := f.Close(); err != nil { // a failed close can mean lost writes
			return err
		}
	}

	if truthPath != "" {
		b, err := json.MarshalIndent(net.Truth(cfg), "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(truthPath, b, 0o644); err != nil { //nolint:gosec // G306: synthetic ground truth, nothing secret; analytics tests read it
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "nodes=%d links=%d packets=%d receptions=%d\n",
		len(net.Nodes), len(net.Links), st.Packets, st.Receptions)
	return nil
}
