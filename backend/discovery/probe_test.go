package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeListModelsAndChatSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/models"):
			if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
				t.Fatalf("auth = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "gpt-4o-mini"}, {"id": "gpt-4o"}},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["model"] != "gpt-4o-mini" {
				t.Fatalf("model = %#v", body["model"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-1",
				"choices": []map[string]any{
					{"message": map[string]string{"role": "assistant", "content": "pong"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	models, err := probeListModels(context.Background(), srv.URL, "openai", "sk-test")
	if err != nil {
		t.Fatalf("probeListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	picked, err := probeChat(context.Background(), srv.URL, "openai", "sk-test", models, "PLUS")
	if err != nil {
		t.Fatalf("probeChat: %v", err)
	}
	if picked != "gpt-4o-mini" {
		t.Fatalf("picked = %q", picked)
	}
}

func TestProbeChatFailsWhenModelsEmpty(t *testing.T) {
	t.Parallel()
	_, err := probeChat(context.Background(), "http://example.invalid", "openai", "sk", nil, "PLUS")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPreferProbeModelsOrdersHintsFirst(t *testing.T) {
	t.Parallel()
	got := preferProbeModels([]string{"claude-3", "gpt-4o-mini", "other"}, "PLUS")
	if len(got) == 0 || got[0] != "gpt-4o-mini" {
		t.Fatalf("got = %#v", got)
	}
}
