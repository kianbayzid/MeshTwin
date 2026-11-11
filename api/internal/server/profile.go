package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// profileIndex is profiles.json indexed by pair, built once per load of the
// validated summary. Entries stay raw JSON, so nothing is reformatted.
type profileIndex struct {
	meta, model, params json.RawMessage
	pairs               map[[2]string]json.RawMessage
}

func indexProfiles(data []byte) (*profileIndex, error) {
	var doc struct {
		Meta     json.RawMessage   `json:"meta"`
		Model    json.RawMessage   `json:"model"`
		Params   json.RawMessage   `json:"params"`
		Profiles []json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode profiles: %w", err)
	}
	idx := &profileIndex{meta: doc.Meta, model: doc.Model, params: doc.Params, pairs: make(map[[2]string]json.RawMessage, len(doc.Profiles))}
	for _, p := range doc.Profiles {
		var ends struct{ A, B string }
		if err := json.Unmarshal(p, &ends); err != nil {
			return nil, fmt.Errorf("decode profile: %w", err)
		}
		idx.pairs[pairKey(ends.A, ends.B)] = p
	}
	return idx, nil
}

// profile serves one link's terrain profile, looked up in either order and
// returned in its stored orientation. profiles.json itself is never served.
func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	region, ok := regionParam(w, r)
	if !ok {
		return
	}
	a, b := r.PathValue("a"), r.PathValue("b")
	if !nodeID(a) || !nodeID(b) {
		writeError(w, http.StatusBadRequest, "bad_request", "a and b must be node ids: 2-8 hex characters")
		return
	}
	// The index has its own cache entry, built from the cached summary, so
	// it can lag a new profiles.json by up to two TTLs (2 min by default).
	idx, err := cached(s, region+"/profiles#index", func() (*profileIndex, error) {
		data, err := s.summary(r.Context(), region, "profiles")
		if err != nil {
			return nil, err
		}
		return indexProfiles(data)
	})
	if err != nil {
		s.writeLoadError(w, r, region, "profiles", err)
		return
	}
	p, ok := idx.pairs[pairKey(a, b)]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no profile for link %s-%s in region %s", a, b, region))
		return
	}
	data, err := json.Marshal(map[string]json.RawMessage{"meta": idx.meta, "model": idx.model, "params": idx.params, "profile": p})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "encode profile")
		return
	}
	writeJSON(w, r, data)
}
