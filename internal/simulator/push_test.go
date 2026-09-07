package simulator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"equipment-telemetry-simulator/internal/model"
)

func TestFilterToirEquipmentKeepsOnlyUUIDs(t *testing.T) {
	assets := []model.Asset{
		{AssetID: "PUMP-101"},
		{AssetID: "0301b754-f675-4cf2-96a2-8a84fc11ebd5"},
		{AssetID: "eq-11111111-1111-1111-1111-111111111111"},
		{AssetID: "toir-22222222-2222-2222-2222-222222222222"},
		{AssetID: "not-uuid"},
	}
	filtered := FilterToirEquipment(assets)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 TOIR assets, got %d", len(filtered))
	}
	if filtered[0].AssetID != "0301b754-f675-4cf2-96a2-8a84fc11ebd5" {
		t.Fatalf("unexpected asset %s", filtered[0].AssetID)
	}
	if filtered[1].AssetID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("expected eq- prefix stripped, got %s", filtered[1].AssetID)
	}
	if filtered[2].AssetID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("expected toir- prefix stripped, got %s", filtered[2].AssetID)
	}
}

func TestPushSkipsWhenNoToirAssets(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := NewPushClient(server.URL, "secret")
	result, err := client.Push(context.Background(), []model.Asset{{AssetID: "PUMP-101"}})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("expected no HTTP call for demo assets")
	}
	if result.Accepted != 0 || result.Rejected != 0 {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestPushRetriesServerErrorsThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Toir-Telemetry-Secret") != "secret" {
			t.Errorf("missing secret header")
		}
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(IngestResult{Accepted: 2, Skipped: 1})
	}))
	t.Cleanup(server.Close)

	client := NewPushClient(server.URL, "secret")
	client.retryBackoff = 0
	result, err := client.Push(context.Background(), []model.Asset{{
		AssetID: "0301b754-f675-4cf2-96a2-8a84fc11ebd5",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
	if result == nil || result.Accepted != 2 || result.Skipped != 1 {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestPushDoesNotRetryUnauthorized(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	client := NewPushClient(server.URL, "secret")
	client.retryBackoff = 0
	_, err := client.Push(context.Background(), []model.Asset{{
		AssetID: "0301b754-f675-4cf2-96a2-8a84fc11ebd5",
	}})
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts.Load())
	}
}

func TestShouldRetryPush(t *testing.T) {
	if !shouldRetryPush(0, context.DeadlineExceeded) {
		t.Fatal("network errors should retry")
	}
	if !shouldRetryPush(http.StatusInternalServerError, nil) {
		t.Fatal("5xx should retry")
	}
	if shouldRetryPush(http.StatusUnauthorized, nil) {
		t.Fatal("401 should not retry")
	}
	if time.Second == 0 {
		t.Fatal("sanity")
	}
}