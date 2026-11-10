package server

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
)

// A query parses one endpoint's parameters and returns the rewrite to apply
// to the stored summary (nil: serve the file as is). Parsing happens before
// storage is touched, so a malformed request costs nothing.
type query func(url.Values) (rewrite func([]byte) ([]byte, error), err error)

var errBadRequest = errors.New("bad request")

func badRequest(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, a...))
}

// topologyQuery: min_confidence in [0.5, 1], default 0.5. It filters links
// only. Node degree and stats stay as the job computed them on its >= 0.5
// graph: they describe the network, the slider only declutters the map, and
// recounting here would make the API a second, divergent analytics engine.
func topologyQuery(q url.Values) (func([]byte) ([]byte, error), error) {
	s := q.Get("min_confidence")
	if s == "" {
		return nil, nil // the job exports only links >= 0.5: nothing to drop
	}
	minConf, err := strconv.ParseFloat(s, 64)
	if err != nil || !(minConf >= 0.5 && minConf <= 1) { // !(...) also rejects NaN
		return nil, badRequest("min_confidence must be a number from 0.5 to 1")
	}
	return func(data []byte) ([]byte, error) {
		return rewriteList(data, "links", func(links []json.RawMessage) ([]json.RawMessage, error) {
			kept := make([]json.RawMessage, 0, len(links))
			for _, l := range links {
				var x struct {
					Confidence float64 `json:"confidence"`
				}
				if err := json.Unmarshal(l, &x); err != nil {
					return nil, err
				}
				if x.Confidence >= minConf {
					kept = append(kept, l)
				}
			}
			return kept, nil
		})
	}, nil
}

// residualsQuery: sort=residual|abs_residual|observations (default
// abs_residual), limit 1-1000 (default 100). Every sort is descending, largest
// first: the links most worth looking at. "observations" sorts by samples, the
// count of measured receptions behind observed_snr_db. summary keeps
// describing all links, not just the page returned.
func residualsQuery(q url.Values) (func([]byte) ([]byte, error), error) {
	sortBy := cmp.Or(q.Get("sort"), "abs_residual")
	if !slices.Contains([]string{"residual", "abs_residual", "observations"}, sortBy) {
		return nil, badRequest("sort must be residual, abs_residual or observations")
	}
	limit, err := intParam(q, "limit", 100, 1000)
	if err != nil {
		return nil, err
	}
	type link struct {
		raw      json.RawMessage
		A, B     string
		Residual float64 `json:"residual_db"`
		Samples  float64 `json:"samples"`
	}
	key := func(l link) float64 {
		switch sortBy {
		case "residual":
			return l.Residual
		case "observations":
			return l.Samples
		}
		return max(l.Residual, -l.Residual)
	}
	return func(data []byte) ([]byte, error) {
		return rewriteList(data, "links", func(raw []json.RawMessage) ([]json.RawMessage, error) {
			links := make([]link, len(raw))
			for i, r := range raw {
				if err := json.Unmarshal(r, &links[i]); err != nil {
					return nil, err
				}
				links[i].raw = r
			}
			slices.SortStableFunc(links, func(x, y link) int {
				return cmp.Or(cmp.Compare(key(y), key(x)), cmp.Compare(x.A, y.A), cmp.Compare(x.B, y.B))
			})
			out := make([]json.RawMessage, 0, min(limit, len(links)))
			for _, l := range links[:min(limit, len(links))] {
				out = append(out, l.raw)
			}
			return out, nil
		})
	}, nil
}

// placementQuery: top 1-50 (default 10) candidates by rank.
func placementQuery(q url.Values) (func([]byte) ([]byte, error), error) {
	top, err := intParam(q, "top", 10, 50)
	if err != nil {
		return nil, err
	}
	return func(data []byte) ([]byte, error) {
		return rewriteList(data, "candidates", func(raw []json.RawMessage) ([]json.RawMessage, error) {
			ranks := make([]int, len(raw))
			for i, r := range raw {
				var x struct {
					Rank int `json:"rank"`
				}
				if err := json.Unmarshal(r, &x); err != nil {
					return nil, err
				}
				ranks[i] = x.Rank
			}
			order := make([]int, len(raw)) // indexes into raw, sorted by rank
			for i := range order {
				order[i] = i
			}
			slices.SortStableFunc(order, func(x, y int) int { return cmp.Compare(ranks[x], ranks[y]) })
			out := make([]json.RawMessage, 0, min(top, len(raw)))
			for _, i := range order[:min(top, len(raw))] {
				out = append(out, raw[i])
			}
			return out, nil
		})
	}, nil
}

func intParam(q url.Values, name string, def, maxVal int) (int, error) {
	s := q.Get(name)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxVal {
		return 0, badRequest("%s must be an integer from 1 to %d", name, maxVal)
	}
	return n, nil
}

// rewriteList replaces the list under key with f(list) and re-encodes the
// document. Items pass through as raw JSON, so nothing is reformatted or lost.
func rewriteList(data []byte, key string, f func([]json.RawMessage) ([]json.RawMessage, error)) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode summary: %w", err)
	}
	var list []json.RawMessage // a missing or null list is treated as empty
	if raw, ok := doc[key]; ok {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("decode %s: %w", key, err)
		}
	}
	list, err := f(list)
	if err != nil {
		return nil, fmt.Errorf("rewrite %s: %w", key, err)
	}
	if doc[key], err = json.Marshal(list); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}
