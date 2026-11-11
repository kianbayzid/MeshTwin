package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"meshtwin/ingest/internal/ingest"
)

type fakeStorage struct{ down bool }

func (f fakeStorage) Put(context.Context, string, []byte) error {
	if f.down {
		return errors.New("storage unreachable")
	}
	return nil
}

func TestReadinessMatrix(t *testing.T) {
	tests := []struct {
		mode        string
		mqttUp      bool
		storageDown bool
		spool       bool
		spoolBroken bool // spool directory vanished: writes fail
		wantCode    int
		wantBody    string
	}{
		{"live", true, false, false, false, 200, "ready\n"},
		{"live", true, true, false, false, 503, "flush failed"},
		{"live", true, false, true, false, 200, "ready\n"},
		{"live", true, true, true, false, 200, "degraded"},
		{"live", true, true, true, true, 503, "flush failed"},
		{"live", false, false, true, false, 503, "mqtt"},
		{"live", false, true, false, false, 503, "mqtt"},
		{"replay", false, false, false, false, 200, "ready\n"},
		{"replay", false, true, false, false, 503, "flush failed"},
		{"replay", false, false, true, false, 200, "ready\n"},
		{"replay", false, true, true, false, 200, "degraded"},
	}
	for _, tc := range tests {
		var store ingest.Store = fakeStorage{down: tc.storageDown}
		var spool *ingest.Spool
		if tc.spool {
			dir := t.TempDir()
			var err error
			if spool, err = ingest.OpenSpool(store, dir, 1<<20, 0); err != nil {
				t.Fatal(err)
			}
			if tc.spoolBroken {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			store = spool
		}
		var mqttUp, flushOK atomic.Bool
		mqttUp.Store(tc.mqttUp)
		// What run's flush does: one Put, flushOK = it succeeded.
		flushOK.Store(store.Put(context.Background(), "receptions/region=SEA/date=2026-11-01/hour=00/part-a.parquet", []byte("x")) == nil)

		h := httpHandler(tc.mode, &mqttUp, &flushOK, storageUpFunc(spool, &flushOK))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("%+v: /readyz = %d %q, want %d containing %q", tc, rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		qos      int
		clean    bool
		clientID string
		ok       bool
	}{
		{0, true, "", true},
		{1, false, "meshtwin-sea-1", true},
		{1, true, "", true},
		{2, true, "", false},
		{1, false, "", false}, // persistent session with a random id: orphaned sessions
	}
	for _, tc := range tests {
		var c config
		c.MQTTQoS, c.MQTT.CleanSession, c.MQTT.ClientID = tc.qos, tc.clean, tc.clientID
		if err := c.validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: validate = %v, want ok=%v", tc, err, tc.ok)
		}
	}
}
