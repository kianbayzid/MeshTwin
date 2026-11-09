package ingest

import (
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"meshtwin/ingest/internal/replay"
	"meshtwin/ingest/internal/synth"
)

// BenchmarkConvertAndEncode measures the ingester's CPU path (JSON parse,
// decode, row build) and Parquet+zstd size against the NDJSON input.
//
//	go test -run=^$ -bench=ConvertAndEncode -benchtime=3x ./internal/ingest
func BenchmarkConvertAndEncode(b *testing.B) {
	cfg := synth.DefaultConfig()
	rng := rand.New(rand.NewPCG(7, 7))
	net, err := synth.NewNetwork(cfg, rng)
	if err != nil {
		b.Fatal(err)
	}
	var recs []replay.Record
	ndjsonBytes := 0
	tr := synth.Traffic{Start: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), Duration: 59 * time.Minute, PacketsPerMin: 300}
	_, err = net.Generate(cfg, tr, rng, func(r replay.Record) error {
		line, _ := json.Marshal(r)
		ndjsonBytes += len(line) + 1
		recs = append(recs, r)
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	var parquetBytes int
	for range b.N {
		rows := make([]Reception, 0, len(recs))
		for _, r := range recs {
			res, err := Convert(r)
			if err != nil {
				b.Fatal(err)
			}
			rows = append(rows, res.Reception)
		}
		_, data, err := encode("receptions", partKey{"SEA", "2026-11-01", "10"}, rows)
		if err != nil {
			b.Fatal(err)
		}
		parquetBytes = len(data)
	}
	b.StopTimer()
	perIter := b.Elapsed().Seconds() / float64(b.N)
	b.ReportMetric(float64(len(recs)), "rows")
	b.ReportMetric(float64(len(recs))/perIter, "rows/s")
	b.ReportMetric(float64(ndjsonBytes)/float64(len(recs)), "ndjson_B/row")
	b.ReportMetric(float64(parquetBytes)/float64(len(recs)), "parquet_B/row")
	b.ReportMetric(float64(ndjsonBytes)/float64(parquetBytes), "compression_x")
}
