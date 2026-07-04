package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchUsersETagServesCacheOn304(t *testing.T) {
	const etag = `"users-v1"`
	var requests int
	var got304 bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == etag {
			got304 = true
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		json.NewEncoder(w).Encode(UserListResponse{Users: []UserResponse{{ID: 7, UUID: "u7"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	first, err := c.FetchUsers(context.Background(), 1, "shadowsocks")
	if err != nil {
		t.Fatalf("first FetchUsers: %v", err)
	}
	second, err := c.FetchUsers(context.Background(), 1, "shadowsocks")
	if err != nil {
		t.Fatalf("second FetchUsers: %v", err)
	}

	if !got304 {
		t.Fatal("second request should have sent If-None-Match and received 304")
	}
	if requests != 2 {
		t.Fatalf("expected 2 requests, got %d", requests)
	}
	if len(first) != 1 || len(second) != 1 || first[0].UUID != second[0].UUID {
		t.Fatalf("304 should serve the cached body: first=%v second=%v", first, second)
	}
}

func mockPanel(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// XBoard reports the port as server_port.
		w.Write([]byte(`{"protocol":"shadowsocks","server_port":443,"cipher":"aes-256-gcm"}`))
	})
	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(UserListResponse{Users: []UserResponse{
			{ID: 1, UUID: "u1", SpeedLimit: 0, DeviceLimit: 3},
			{ID: 2, UUID: "u2"},
		}})
	})
	mux.HandleFunc("/api/v1/server/UniProxy/push", func(w http.ResponseWriter, r *http.Request) {
		// Panel expects an object keyed by user id: {"1":[u,d]}.
		var body map[string][2]uint64
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/server/UniProxy/alive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(Options{ApiHost: baseURL, ApiKey: "test-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestFetchNodeConfig(t *testing.T) {
	srv := mockPanel(t)
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	cfg, err := c.FetchNodeConfig(context.Background(), 1, "shadowsocks")
	if err != nil {
		t.Fatalf("FetchNodeConfig: %v", err)
	}
	if cfg.ListenPort() != 443 || cfg.Cipher != "aes-256-gcm" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestFetchUsersBareAndWrapped(t *testing.T) {
	srv := mockPanel(t)
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	users, err := c.FetchUsers(context.Background(), 1, "shadowsocks")
	if err != nil {
		t.Fatalf("FetchUsers: %v", err)
	}
	if len(users) != 2 || users[0].UUID != "u1" {
		t.Fatalf("unexpected users: %+v", users)
	}
}

func TestPushTrafficAndAlive(t *testing.T) {
	srv := mockPanel(t)
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	err := c.PushTraffic(context.Background(), 1, "shadowsocks", []TrafficRecord{{UID: 1, Upload: 100, Download: 200}})
	if err != nil {
		t.Fatalf("PushTraffic: %v", err)
	}
	err = c.ReportAlive(context.Background(), 1, "shadowsocks", []AliveRecord{{UID: 1, IP: "1.2.3.4"}})
	if err != nil {
		t.Fatalf("ReportAlive: %v", err)
	}
}

func TestUnauthorized(t *testing.T) {
	srv := mockPanel(t)
	defer srv.Close()
	c, err := New(Options{ApiHost: srv.URL, ApiKey: "wrong-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.FetchNodeConfig(context.Background(), 1, "shadowsocks"); err == nil {
		t.Fatal("expected error for wrong api key")
	}
}

func TestWithRetryRecoversAfterFailures(t *testing.T) {
	attempts := 0
	err := WithRetry(context.Background(), nil, RetryConfig{InitialDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}, "test-op",
		func(ctx context.Context) error {
			attempts++
			if attempts < 3 {
				return errors.New("transient failure")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("WithRetry: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	attempts := 0
	err := WithRetry(context.Background(), nil, RetryConfig{InitialDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, MaxAttempts: 2}, "test-op",
		func(ctx context.Context) error {
			attempts++
			return errors.New("permanent failure")
		})
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}
