// Command anonymize turns a raw observer capture into one that is safe to
// commit as test data. See internal/anonymize for exactly what changes.
//
//	anonymize -in ../testdata/recorded/raw/sea.ndjson -out ../testdata/recorded/sea-anon.ndjson
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"meshtwin/ingest/internal/anonymize"
	"meshtwin/ingest/internal/replay"
)

func main() {
	in := flag.String("in", "", "raw NDJSON capture (private)")
	out := flag.String("out", "", "anonymized NDJSON to write")
	flag.Parse()
	if *in == "" || *out == "" {
		log.Fatal("usage: anonymize -in raw.ndjson -out anon.ndjson")
	}
	if err := run(*in, *out); err != nil {
		log.Fatal(err)
	}
}

func run(inPath, outPath string) error {
	key := make([]byte, 32) // random, used once, never written anywhere: the mapping can't be reversed
	if _, err := rand.Read(key); err != nil {
		return err
	}
	a := anonymize.New(key)

	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()
	o, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer o.Close()
	bw := bufio.NewWriter(o)
	enc := json.NewEncoder(bw)

	var st anonymize.Stats
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r replay.Record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			st.Dropped++
			continue
		}
		ar, err := a.Record(r, &st)
		if errors.Is(err, anonymize.ErrDrop) {
			continue
		}
		if err != nil {
			return err
		}
		if err := enc.Encode(ar); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := o.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "kept=%d dropped=%d hash_mismatches_in_original=%d\n", st.Kept, st.Dropped, st.HashMismatchesInOriginal)
	return nil
}
