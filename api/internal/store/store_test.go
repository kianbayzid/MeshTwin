package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidRegion(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"SEA", true}, {"PDX1", true}, {"ABCDEFGH", true},
		{"S", false}, {"ABCDEFGHI", false}, {"sea", false}, {"..", false},
		{"SE/A", false}, {"X:Y", false}, {"SEA!", false}, {"", false}, {"SÉA", false},
	} {
		if got := ValidRegion(tc.in); got != tc.want {
			t.Errorf("ValidRegion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseKey(t *testing.T) {
	for _, tc := range []struct {
		key, region, name string
		ok                bool
	}{
		{"summaries/region=SEA/topology.json", "SEA", "topology", true},
		{"summaries/region=SEA/placement.json", "SEA", "placement", true},
		{"summaries/region=SEA/other.json", "", "", false},
		{"summaries/region=SEA/topology.json.tmp", "", "", false},
		{"summaries/region=s!/topology.json", "", "", false},
		{"summaries/SEA/topology.json", "", "", false},
		{"results/region=SEA/topology.json", "", "", false},
		{"summaries/region=SEA/x/topology.json", "", "", false},
	} {
		region, name, ok := parseKey(tc.key)
		if region != tc.region || name != tc.name || ok != tc.ok {
			t.Errorf("parseKey(%q) = %q, %q, %v", tc.key, region, name, ok)
		}
	}
}

func TestLocal(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("summaries/region=SEA/residuals.json", "{}")
	write("summaries/region=SEA/topology.json", `{"x":1}`)
	write("summaries/region=SEA/notes.txt", "ignored")
	write("summaries/region=PDX/placement.json", "{}")
	l := Local{Root: root}
	ctx := context.Background()

	got, err := l.Get(ctx, "SEA", "topology")
	if err != nil || string(got) != `{"x":1}` {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if _, err := l.Get(ctx, "SEA", "placement"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing summary: err = %v, want ErrNotFound", err)
	}
	list, err := l.List(ctx)
	want := map[string][]string{"SEA": {"topology", "residuals"}, "PDX": {"placement"}}
	if err != nil || !reflect.DeepEqual(list, want) {
		t.Errorf("List = %v, %v; want %v", list, err, want)
	}
	if _, err := (Local{Root: filepath.Join(root, "nope")}).List(ctx); err == nil {
		t.Error("List on a missing root should fail (readiness)")
	}
}

func TestSizeCap(t *testing.T) {
	for _, tc := range []struct {
		size int
		err  error
	}{{MaxBytes, nil}, {MaxBytes + 1, ErrTooLarge}} {
		root := t.TempDir()
		dir := filepath.Join(root, "summaries", "region=SEA")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "topology.json"), make([]byte, tc.size), 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := Local{Root: root}.Get(context.Background(), "SEA", "topology")
		if !errors.Is(err, tc.err) || (err == nil && len(b) != tc.size) {
			t.Errorf("size %d: len %d, err %v; want err %v", tc.size, len(b), err, tc.err)
		}
	}
}

// TestLocalSymlinks: OpenInRoot must refuse symlinks (file or region dir) that
// lead outside Root, and still follow relative ones that stay inside it.
// Absolute targets are refused even when they point inside Root (os.Root rule).
func TestLocalSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	mk := func(p, body string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, name); err != nil {
			t.Skipf("cannot create symlinks here (Windows without privilege?): %v", err)
		}
	}
	mk(filepath.Join(outside, "secret.json"), `"outside"`)
	mk(filepath.Join(outside, "dir", "topology.json"), `"outside"`)
	mk(filepath.Join(root, "real", "topology.json"), `"inside"`)
	sums := filepath.Join(root, "summaries")
	link(filepath.Join(outside, "secret.json"), filepath.Join(sums, "region=SEA", "topology.json"))
	link(filepath.Join(outside, "dir"), filepath.Join(sums, "region=PDX"))
	link(filepath.Join("..", "..", "real", "topology.json"), filepath.Join(sums, "region=BOS", "topology.json"))
	link(filepath.Join("..", "real"), filepath.Join(sums, "region=LAX"))
	link(filepath.Join(root, "real"), filepath.Join(sums, "region=ORD"))

	l := Local{Root: root}
	for _, tc := range []struct {
		name, region string
		ok           bool
	}{
		{"file symlink escaping root", "SEA", false},
		{"region dir symlink escaping root", "PDX", false},
		{"file symlink inside root", "BOS", true},
		{"region dir symlink inside root", "LAX", true},
		{"absolute symlink, even inside root", "ORD", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := l.Get(context.Background(), tc.region, "topology")
			if tc.ok && (err != nil || string(got) != `"inside"`) {
				t.Errorf("Get = %q, %v; want the in-root file", got, err)
			}
			if !tc.ok && (err == nil || len(got) != 0) {
				t.Errorf("Get = %q, %v; want refusal", got, err)
			}
		})
	}
}
