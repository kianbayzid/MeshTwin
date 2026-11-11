// Command meshdecode prints a field-by-field breakdown of a MeshCore packet.
//
//	meshdecode 1102353405...           # hex as an argument
//	echo 1102353405... | meshdecode    # or on stdin, one packet per line
package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"meshtwin/ingest/internal/meshcore"
)

var routeNames = []string{"TRANSPORT_FLOOD", "FLOOD", "DIRECT", "TRANSPORT_DIRECT"}

func main() {
	inputs := os.Args[1:]
	if len(inputs) == 0 {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if s := strings.TrimSpace(sc.Text()); s != "" {
				inputs = append(inputs, s)
			}
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintln(os.Stderr, "reading stdin:", err)
			os.Exit(1)
		}
	}
	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: meshdecode <hex packet> ... (or hex lines on stdin)")
		os.Exit(2)
	}
	failed := false
	for _, in := range inputs {
		if err := describe(os.Stdout, in); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			failed = true
		}
		fmt.Println()
	}
	if failed {
		os.Exit(1)
	}
}

func describe(w io.Writer, hexStr string) error {
	raw, err := hex.DecodeString(strings.ReplaceAll(hexStr, " ", ""))
	if err != nil {
		return fmt.Errorf("not hex: %w", err)
	}
	p, err := meshcore.Decode(raw)
	if err != nil {
		return err
	}
	h := meshcore.Hash(p)
	row := func(k, v string, args ...any) { _, _ = fmt.Fprintf(w, "  %-16s %s\n", k, fmt.Sprintf(v, args...)) }

	_, _ = fmt.Fprintf(w, "%d bytes, packet hash %X\n", len(raw), h)
	row("header", "0x%02X  route=%s type=%v version=%d", raw[0], routeNames[p.Route], p.Type, p.Version)
	if p.HasTransportCodes() {
		row("transport codes", "%04X %04X", p.TransportCodes[0], p.TransportCodes[1])
	}
	row("path", "%d hop(s) x %d byte(s)", p.HopCount(), p.PathHashSize)
	if snrs := p.TraceSNRs(); snrs != nil {
		row("  trace SNRs", "%v dB  (TRACE: path holds SNR, not hashes)", snrs)
	} else {
		var hops []string
		for _, hop := range p.Hops() {
			hops = append(hops, fmt.Sprintf("%X", hop))
		}
		note := "hops still to go (direct: each hop removes itself)"
		if p.IsFlood() {
			note = "relays, oldest first (flood: each repeater appends itself)"
		}
		if len(hops) > 0 {
			row("  hops", "%s  <- %s", strings.Join(hops, " -> "), note)
		}
	}
	row("payload", "%d bytes", len(p.Payload))
	if src, dst := p.Endpoints(); src != nil || dst != nil {
		row("  src / dst", "%s / %s  (plaintext key or 1-byte key prefix)", orDash(src), orDash(dst))
	}

	if p.Type == meshcore.PayloadAdvert {
		a, err := meshcore.ParseAdvert(p.Payload)
		if err != nil {
			row("  advert", "malformed: %v", err)
			return nil
		}
		row("  advert name", "%q", a.Name)
		row("  advert role", "%d", a.Role)
		row("  advert time", "%d (sender clock)", a.Timestamp)
		if a.HasLocation {
			// Shown exactly: this tool is for the operator, not for publication.
			row("  advert lat/lon", "%.6f, %.6f", a.Lat(), a.Lon())
		}
	} else if p.Type != meshcore.PayloadAck && p.Type != meshcore.PayloadTrace {
		row("  contents", "encrypted; not decrypted (by design)")
	}
	return nil
}

func orDash(b []byte) string {
	if b == nil {
		return "-"
	}
	return fmt.Sprintf("%X", b)
}
