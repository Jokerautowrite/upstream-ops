package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/discovery"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

type groupDiscoveryChannelServiceStub struct{}

func (groupDiscoveryChannelServiceStub) ListAPIKeyGroups(context.Context, uint) ([]connector.APIKeyGroup, error) {
	return nil, nil
}

func (groupDiscoveryChannelServiceStub) ListAPIKeys(context.Context, uint, connector.APIKeyQuery) (*connector.APIKeyPage, error) {
	return &connector.APIKeyPage{}, nil
}

func (groupDiscoveryChannelServiceStub) CreateAPIKey(context.Context, uint, connector.APIKeyCreateRequest) (*connector.APIKey, error) {
	return nil, nil
}

func (groupDiscoveryChannelServiceStub) UpdateAPIKey(context.Context, uint, int64, connector.APIKeyUpdateRequest) (*connector.APIKey, error) {
	return nil, nil
}

func (groupDiscoveryChannelServiceStub) RevealAPIKey(context.Context, uint, int64) (string, error) {
	return "", nil
}

func TestGroupDiscoveryApproveEndpointBindsSnakeCaseInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	channels := storage.NewChannels(db)
	source := &storage.Channel{
		Name:           "source",
		Type:           storage.ChannelTypeSub2API,
		SiteURL:        "https://source.example",
		Username:       "user",
		PasswordCipher: "cipher",
		MonitorEnabled: true,
	}
	if err := channels.Create(source); err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	candidate := &storage.GroupDiscoveryCandidate{
		SourceChannelID:      source.ID,
		SourceChannelName:    source.Name,
		SourceGroupKey:       "name:source-low",
		SourceGroupName:      "source-low",
		Ratio:                0.1,
		Status:               "pending",
		TargetGroupIDsJSON:   "[]",
		TargetGroupNamesJSON: "[]",
		Platform:             "openai",
		AccountName:          "source-low",
		Concurrency:          10,
		Weight:               1,
		DiscoveredAt:         time.Now(),
		LastSeenAt:           time.Now(),
	}
	if err := storage.NewGroupDiscoveryCandidates(db).Create(candidate); err != nil {
		t.Fatalf("create candidate: %v", err)
	}

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/groups/all" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":101,"name":"target-low","platform":"openai","ratio":0.1,"status":"active","sort":1}]}`))
	}))
	defer admin.Close()

	cipher, err := crypto.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	adminKey, err := cipher.Encrypt("admin-key")
	if err != nil {
		t.Fatalf("encrypt admin key: %v", err)
	}
	target := &storage.UpstreamSyncTarget{
		Name:              "target",
		BaseURL:           admin.URL,
		AdminAPIKeyCipher: adminKey,
		Enabled:           true,
	}
	if err := storage.NewUpstreamSyncTargets(db).Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	service := discovery.New(
		channels,
		storage.NewGroupDiscoveryCandidates(db),
		storage.NewUpstreamSyncTargets(db),
		storage.NewUpstreamSyncTargetGroups(db),
		cipher,
		groupDiscoveryChannelServiceStub{},
	)
	router := gin.New()
	registerGroupDiscovery(router.Group("/api"), &Deps{GroupDiscovery: service})
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/upstream-sync/group-discovery/candidates/"+strconv.FormatUint(uint64(candidate.ID), 10)+"/approve",
		strings.NewReader(`{"target_id":1,"target_group_ids":[101],"account_name":"target-source-low","platform":"openai","concurrency":7,"weight":3}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			Status         string  `json:"status"`
			TargetID       *uint   `json:"target_id"`
			TargetGroupIDs []int64 `json:"target_group_ids"`
			AccountName    string  `json:"account_name"`
			Concurrency    int     `json:"concurrency"`
			Weight         int     `json:"weight"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode approval response: %v", err)
	}
	if response.Data.Status != "approved" || response.Data.TargetID == nil || *response.Data.TargetID != target.ID {
		t.Fatalf("approval response = %#v", response.Data)
	}
	if len(response.Data.TargetGroupIDs) != 1 || response.Data.TargetGroupIDs[0] != 101 || response.Data.AccountName != "target-source-low" || response.Data.Concurrency != 7 || response.Data.Weight != 3 {
		t.Fatalf("approval fields = %#v", response.Data)
	}
}
