package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPairSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/pair" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "ABC234" {
			t.Errorf("unexpected code: %s", body["code"])
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"machineId": "m-123",
			"authToken": "tok-abc",
			"relayUrl":  "wss://relay.test/ws",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.Pair(PairRequest{
		Code:          "ABC234",
		Hostname:      "test",
		OS:            "darwin",
		Arch:          "arm64",
		DaemonVersion: "0.1.0",
	})
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if resp.MachineID != "m-123" || resp.AuthToken != "tok-abc" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestPairNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.Pair(PairRequest{
		Code:          "BADBAD",
		Hostname:      "h",
		OS:            "darwin",
		Arch:          "arm64",
		DaemonVersion: "0.1.0",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
