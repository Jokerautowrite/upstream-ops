package sub2api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPoolAccountIdentityHashesRawKeyAndReadsCompatibilityFingerprint(t *testing.T) {
	raw := PoolAccount{Account: AdminAccount{
		Credentials: map[string]any{
			"base_url": "https://upstream.example/v1",
			"api_key":  "pool-key",
		},
	}}
	identity := raw.Identity()
	if !identity.FingerprintSeen || identity.APIKeySHA256 != "b668c780f9bf371c3d64afaa1d666aba21e5b80dace2da764bb0c92495578066" {
		t.Fatalf("raw identity = %#v", identity)
	}
	if identity.BaseURL != "https://upstream.example/v1" {
		t.Fatalf("base url = %q", identity.BaseURL)
	}

	compatibility := PoolAccount{
		Account:           AdminAccount{Credentials: map[string]any{"base_url": "https://upstream.example/v1"}},
		APIKeyFingerprint: "B668C780F9BF371C3D64AFAA1D666ABA21E5B80DACE2DA764BB0C92495578066",
	}
	identity = compatibility.Identity()
	if !identity.FingerprintSeen || identity.APIKeySHA256 != "b668c780f9bf371c3d64afaa1d666aba21e5b80dace2da764bb0c92495578066" {
		t.Fatalf("compatibility identity = %#v", identity)
	}
}

func TestDecodePoolAccountReadsSafeHealthAndTodayStats(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	account, err := decodePoolAccount([]byte(`{
			"id": 7,
			"status": "active",
			"current_concurrency": 3,
			"rate_limited": false,
			"temp_unschedulable": true,
			"overload_until": "` + future + `",
		"today_requests": 17,
		"today_actual_cost": 2.5,
		"credential_fingerprint": {
			"api_key_sha256": "b668c780f9bf371c3d64afaa1d666aba21e5b80dace2da764bb0c92495578066"
		},
		"credentials": {"base_url": "https://upstream.example/v1"}
	}`))
	if err != nil {
		t.Fatalf("decode pool account: %v", err)
	}
	if !account.Identity().FingerprintSeen {
		t.Fatalf("fingerprint was not decoded: %#v", account)
	}
	if account.Health.CurrentConcurrency != 3 ||
		account.Health.RateLimited ||
		!account.Health.TemporarilyUnschedulable ||
		!account.Health.Overloaded {
		t.Fatalf("health = %#v", account.Health)
	}
	if account.Stats.TodayRequests == nil || *account.Stats.TodayRequests != 17 ||
		account.Stats.TodayCost == nil || *account.Stats.TodayCost != 2.5 {
		t.Fatalf("stats = %#v", account.Stats)
	}
}

func TestDecodePoolAccountIgnoresExpiredRuntimeWindows(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	account, err := decodePoolAccount([]byte(`{
		"id": 8,
		"rate_limited_at": "2026-01-01T00:00:00Z",
		"rate_limit_reset_at": "` + past + `",
		"temp_unschedulable_until": "` + past + `",
		"overload_until": "` + past + `"
	}`))
	if err != nil {
		t.Fatalf("decode pool account: %v", err)
	}
	if account.Health.RateLimited || account.Health.TemporarilyUnschedulable || account.Health.Overloaded {
		t.Fatalf("expired health window stayed active: %#v", account.Health)
	}
}

func TestAdminPoolOperationsUsePaginationGetAndPriorityOnlyUpdate(t *testing.T) {
	mux := http.NewServeMux()
	var patchedPriority bool
	var patchedSchedulable bool
	var fetchedTodayStats bool
	mux.HandleFunc("/api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "100" {
			t.Fatalf("query = %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"items": []map[string]any{{
				"id": 7,
				"credentials": map[string]any{
					"base_url": "https://upstream.example/v1",
					"api_key":  "pool-key",
				},
			}}},
		})
	})
	mux.HandleFunc("/api/v1/admin/accounts/today-stats/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			AccountIDs []int64 `json:"account_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode today stats body: %v", err)
		}
		if len(body.AccountIDs) != 1 || body.AccountIDs[0] != 7 {
			t.Fatalf("today stats account ids = %#v", body.AccountIDs)
		}
		fetchedTodayStats = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"stats": map[string]any{
					"7": map[string]any{"requests": 12, "cost": 3.5},
				},
			},
		})
	})
	mux.HandleFunc("/api/v1/admin/accounts/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"id":          7,
					"credentials": map[string]any{"base_url": "https://upstream.example/v1"},
					"credential_fingerprint": map[string]any{
						"api_key_sha256": "b668c780f9bf371c3d64afaa1d666aba21e5b80dace2da764bb0c92495578066",
					},
				},
			})
		case http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode priority body: %v", err)
			}
			if len(body) != 1 || body["priority"] != float64(20) {
				t.Fatalf("priority-only body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"id": 7, "priority": 20},
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/admin/accounts/7/priority", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode priority patch: %v", err)
		}
		if len(body) != 1 || body["priority"] != float64(20) {
			t.Fatalf("priority patch body = %#v", body)
		}
		patchedPriority = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"id": 7, "priority": 20},
		})
	})
	mux.HandleFunc("/api/v1/admin/accounts/7/schedulable", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode schedulable patch: %v", err)
		}
		if len(body) != 1 || body["schedulable"] != false {
			t.Fatalf("schedulable patch body = %#v", body)
		}
		patchedSchedulable = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"id": 7, "schedulable": false},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewAdminClient()
	target := AdminTarget{BaseURL: srv.URL, APIKey: "admin-key"}
	list, err := client.ListAllPoolAccounts(context.Background(), target)
	if err != nil {
		t.Fatalf("ListAllPoolAccounts: %v", err)
	}
	if len(list) != 1 || !list[0].Identity().FingerprintSeen {
		t.Fatalf("accounts = %#v", list)
	}
	stats, err := client.GetPoolTodayStatsBatch(context.Background(), target, []int64{7})
	if err != nil {
		t.Fatalf("GetPoolTodayStatsBatch: %v", err)
	}
	if !fetchedTodayStats || stats[7].Requests != 12 || stats[7].Cost != 3.5 {
		t.Fatalf("today stats = %#v, fetched=%v", stats, fetchedTodayStats)
	}
	account, err := client.GetPoolAccount(context.Background(), target, 7)
	if err != nil {
		t.Fatalf("GetPoolAccount: %v", err)
	}
	if account.Identity().APIKeySHA256 == "" {
		t.Fatalf("get account identity missing: %#v", account)
	}
	updated, err := client.UpdatePoolAccountPriority(context.Background(), target, 7, 20)
	if err != nil {
		t.Fatalf("UpdatePoolAccountPriority: %v", err)
	}
	if updated.Account.Priority != 20 {
		t.Fatalf("updated priority = %d", updated.Account.Priority)
	}
	if !patchedPriority {
		t.Fatal("priority PATCH endpoint was not used")
	}
	updated, err = client.SetPoolAccountSchedulable(context.Background(), target, 7, false)
	if err != nil {
		t.Fatalf("SetPoolAccountSchedulable: %v", err)
	}
	if updated.Account.Schedulable || !patchedSchedulable {
		t.Fatalf("schedulable update = %#v, patched=%v", updated, patchedSchedulable)
	}
}
