package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/bejix/upstream-ops/backend/sub2pool"
	"gorm.io/gorm"
)

type passthroughCipher struct{}

func (passthroughCipher) Decrypt(ciphertext string) (string, error) {
	return ciphertext, nil
}

type discoveryChannelServiceFake struct {
	mu sync.Mutex

	groupsByChannel map[uint][]connector.APIKeyGroup
	keysByChannel   map[uint][]connector.APIKey
	nextKeyID       int64

	groupCallsByChannel  map[uint]int
	groupErrorsByChannel map[uint]error
	listKeyCalls         int
	createCalls          int
	updateCalls          int
	revealCalls          int
	lastCreate           connector.APIKeyCreateRequest
	lastUpdate           connector.APIKeyUpdateRequest
}

func newDiscoveryChannelServiceFake() *discoveryChannelServiceFake {
	return &discoveryChannelServiceFake{
		groupsByChannel:      map[uint][]connector.APIKeyGroup{},
		keysByChannel:        map[uint][]connector.APIKey{},
		nextKeyID:            100,
		groupCallsByChannel:  map[uint]int{},
		groupErrorsByChannel: map[uint]error{},
	}
}

func (f *discoveryChannelServiceFake) ListAPIKeyGroups(_ context.Context, channelID uint) ([]connector.APIKeyGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupCallsByChannel[channelID]++
	if err := f.groupErrorsByChannel[channelID]; err != nil {
		return nil, err
	}
	return append([]connector.APIKeyGroup(nil), f.groupsByChannel[channelID]...), nil
}

func (f *discoveryChannelServiceFake) ListAPIKeys(_ context.Context, channelID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listKeyCalls++
	items := append([]connector.APIKey(nil), f.keysByChannel[channelID]...)
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = len(items)
	}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return &connector.APIKeyPage{Page: page, PageSize: pageSize, Total: int64(len(items)), Pages: 1}, nil
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	pages := (len(items) + pageSize - 1) / pageSize
	return &connector.APIKeyPage{
		Items:    items[start:end],
		Page:     page,
		PageSize: pageSize,
		Total:    int64(len(items)),
		Pages:    pages,
	}, nil
}

func (f *discoveryChannelServiceFake) CreateAPIKey(_ context.Context, channelID uint, req connector.APIKeyCreateRequest) (*connector.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.lastCreate = req
	key := connector.APIKey{
		ID:             f.nextKeyID,
		Key:            fmt.Sprintf("sk-discovery-%d", f.nextKeyID),
		Name:           req.Name,
		Group:          req.Group,
		GroupName:      req.Group,
		GroupID:        req.GroupID,
		UnlimitedQuota: req.UnlimitedQuota != nil && *req.UnlimitedQuota,
	}
	if req.ExpiredTime != nil {
		key.ExpiredTime = *req.ExpiredTime
	}
	f.nextKeyID++
	f.keysByChannel[channelID] = append(f.keysByChannel[channelID], key)
	return &key, nil
}

func (f *discoveryChannelServiceFake) UpdateAPIKey(_ context.Context, channelID uint, keyID int64, req connector.APIKeyUpdateRequest) (*connector.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	f.lastUpdate = req
	for i := range f.keysByChannel[channelID] {
		key := &f.keysByChannel[channelID][i]
		if key.ID != keyID {
			continue
		}
		if req.Name != nil {
			key.Name = *req.Name
		}
		if req.Group != nil {
			key.Group = *req.Group
			key.GroupName = *req.Group
		}
		key.GroupID = req.GroupID
		if req.UnlimitedQuota != nil {
			key.UnlimitedQuota = *req.UnlimitedQuota
		}
		if req.ExpiredTime != nil {
			key.ExpiredTime = *req.ExpiredTime
		}
		copy := *key
		return &copy, nil
	}
	return nil, fmt.Errorf("source API key not found: %d", keyID)
}

func (f *discoveryChannelServiceFake) RevealAPIKey(_ context.Context, channelID uint, keyID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revealCalls++
	for _, key := range f.keysByChannel[channelID] {
		if key.ID == keyID {
			return key.Key, nil
		}
	}
	return "", fmt.Errorf("source API key not found: %d", keyID)
}

type discoveryAdminServer struct {
	t *testing.T

	mu sync.Mutex

	groups []map[string]any

	accounts      map[int64]map[string]any
	nextAccountID int64

	groupCalls       int
	accountListCalls int
	createCalls      int
	updateCalls      int
	syncModelCalls   int
	schedulableCalls []bool

	failModelSyncCount int
	models             []string
}

func newDiscoveryAdminServer(t *testing.T) (*httptest.Server, *discoveryAdminServer) {
	t.Helper()
	state := &discoveryAdminServer{
		t: t,
		groups: []map[string]any{
			{"id": int64(101), "name": "target-low", "platform": "openai", "ratio": 0.1, "status": "active", "sort": 1},
			{"id": int64(102), "name": "target-inactive", "platform": "openai", "ratio": 1.0, "status": "inactive", "sort": 2},
		},
		accounts:      map[int64]map[string]any{},
		nextAccountID: 10,
		models:        []string{"gpt-test", "gpt-test-2"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/groups/all", state.handleGroups)
	mux.HandleFunc("/api/v1/admin/accounts", state.handleAccounts)
	mux.HandleFunc("/api/v1/admin/accounts/", state.handleAccount)
	return httptest.NewServer(mux), state
}

func (s *discoveryAdminServer) requireAdminKey(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("x-api-key") == "admin-key" {
		return true
	}
	s.t.Errorf("x-api-key = %q, want admin-key", r.Header.Get("x-api-key"))
	http.Error(w, "missing admin key", http.StatusUnauthorized)
	return false
}

func (s *discoveryAdminServer) handleGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminKey(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	s.groupCalls++
	groups := append([]map[string]any(nil), s.groups...)
	s.mu.Unlock()
	writeDiscoveryJSON(w, map[string]any{"code": 0, "data": groups})
}

func (s *discoveryAdminServer) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminKey(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		s.accountListCalls++
		ids := make([]int64, 0, len(s.accounts))
		for id := range s.accounts {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		items := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			items = append(items, cloneDiscoveryObject(s.accounts[id]))
		}
		s.mu.Unlock()
		writeDiscoveryJSON(w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
	case http.MethodPost:
		body, ok := decodeDiscoveryObject(testerFromRequest(s.t), w, r)
		if !ok {
			return
		}
		s.mu.Lock()
		id := s.nextAccountID
		s.nextAccountID++
		body["id"] = id
		s.accounts[id] = body
		s.createCalls++
		out := cloneDiscoveryObject(body)
		s.mu.Unlock()
		writeDiscoveryJSON(w, map[string]any{"code": 0, "data": out})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *discoveryAdminServer) handleAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminKey(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		s.t.Errorf("parse account id %q: %v", parts[0], err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	action := strings.Join(parts[1:], "/")
	switch action {
	case "schedulable":
		s.handleSchedulable(w, r, id)
	case "models/sync-upstream":
		s.handleModelSync(w, r, id)
	case "":
		s.handleAccountUpdate(w, r, id)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *discoveryAdminServer) handleSchedulable(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Schedulable bool `json:"schedulable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.t.Errorf("decode schedulable body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	account, ok := s.accounts[id]
	if ok {
		account["schedulable"] = body.Schedulable
		s.schedulableCalls = append(s.schedulableCalls, body.Schedulable)
	}
	out := cloneDiscoveryObject(account)
	s.mu.Unlock()
	if !ok {
		writeDiscoveryError(w, http.StatusNotFound, "account not found")
		return
	}
	writeDiscoveryJSON(w, map[string]any{"code": 0, "data": out})
}

func (s *discoveryAdminServer) handleModelSync(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	_, ok := s.accounts[id]
	s.syncModelCalls++
	fail := s.failModelSyncCount > 0
	if fail {
		s.failModelSyncCount--
	}
	models := append([]string(nil), s.models...)
	s.mu.Unlock()
	if !ok {
		writeDiscoveryError(w, http.StatusNotFound, "account not found")
		return
	}
	if fail {
		writeDiscoveryJSON(w, map[string]any{"code": 1, "message": "model sync failed"})
		return
	}
	writeDiscoveryJSON(w, map[string]any{"code": 0, "data": map[string]any{"models": models}})
}

func (s *discoveryAdminServer) handleAccountUpdate(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, ok := decodeDiscoveryObject(testerFromRequest(s.t), w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	previous, exists := s.accounts[id]
	if exists {
		if schedulable, found := previous["schedulable"]; found {
			body["schedulable"] = schedulable
		}
		body["id"] = id
		s.accounts[id] = body
		s.updateCalls++
	}
	out := cloneDiscoveryObject(body)
	s.mu.Unlock()
	if !exists {
		writeDiscoveryError(w, http.StatusNotFound, "account not found")
		return
	}
	writeDiscoveryJSON(w, map[string]any{"code": 0, "data": out})
}

func (s *discoveryAdminServer) addManualAccount(id int64, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[id] = map[string]any{
		"id":          id,
		"name":        name,
		"platform":    "openai",
		"type":        "apikey",
		"status":      "active",
		"notes":       "manually managed",
		"credentials": map[string]any{"api_key": "manual-key"},
	}
}

func (s *discoveryAdminServer) account(id int64) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDiscoveryObject(s.accounts[id])
}

func cloneDiscoveryObject(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	copy := make(map[string]any, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}

func testerFromRequest(t *testing.T) *testing.T {
	return t
}

func decodeDiscoveryObject(t *testing.T, w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode request body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

func writeDiscoveryJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeDiscoveryError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	writeDiscoveryJSON(w, map[string]any{"code": 1, "message": message})
}

func openDiscoveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(storage.DBConfig{
		Driver: storage.DBDriverSQLite,
		Path:   filepath.Join(t.TempDir(), "discovery-test.db"),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newDiscoveryTestService(db *gorm.DB, channelSvc *discoveryChannelServiceFake) *Service {
	return New(
		storage.NewChannels(db),
		storage.NewGroupDiscoveryCandidates(db),
		storage.NewUpstreamSyncTargets(db),
		storage.NewUpstreamSyncTargetGroups(db),
		passthroughCipher{},
		channelSvc,
	)
}

func seedDiscoveryChannel(t *testing.T, db *gorm.DB, name string, channelType storage.ChannelType, monitorEnabled bool) *storage.Channel {
	t.Helper()
	channel := &storage.Channel{
		Name:           name,
		Type:           channelType,
		SiteURL:        "https://" + name + ".example",
		Username:       "user",
		PasswordCipher: "cipher",
		MonitorEnabled: monitorEnabled,
	}
	if err := storage.NewChannels(db).Create(channel); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if !monitorEnabled {
		if err := db.Model(channel).Update("monitor_enabled", false).Error; err != nil {
			t.Fatalf("disable channel monitoring: %v", err)
		}
		channel.MonitorEnabled = false
	}
	return channel
}

func seedDiscoveryTarget(t *testing.T, db *gorm.DB, baseURL string) *storage.UpstreamSyncTarget {
	t.Helper()
	target := &storage.UpstreamSyncTarget{
		Name:              "target",
		BaseURL:           baseURL,
		AdminAPIKeyCipher: "admin-key",
		Enabled:           true,
	}
	if err := storage.NewUpstreamSyncTargets(db).Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	return target
}

// seedDiscoveredGroup scans a single group and returns its persisted candidate.
// The candidate ID exists only after the scan, so callers must reload it.
func seedDiscoveredGroup(t *testing.T, svc *Service, source *storage.Channel, sourceGroup connector.APIKeyGroup) *storage.GroupDiscoveryCandidate {
	t.Helper()
	fake, ok := svc.channelSvc.(*discoveryChannelServiceFake)
	if !ok {
		t.Fatal("unexpected channel service fake")
	}
	fake.mu.Lock()
	fake.groupsByChannel[source.ID] = []connector.APIKeyGroup{sourceGroup}
	fake.mu.Unlock()
	result, err := svc.Scan(context.Background(), ScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if result.SelectedCandidates != 1 {
		t.Fatalf("scan selected candidates = %d, want 1", result.SelectedCandidates)
	}
	item, err := svc.candidates.FindBySource(source.ID, sourceGroupKey(sourceGroup))
	if err != nil {
		t.Fatalf("find scanned candidate: %v", err)
	}
	if item.AccountName == "" {
		item.AccountName = defaultAccountName(item.SourceGroupName, item.ID)
		if err := svc.candidates.Update(item); err != nil {
			t.Fatalf("name seeded candidate: %v", err)
		}
	}
	return item
}

func approveDiscoveryCandidate(t *testing.T, svc *Service, candidate *storage.GroupDiscoveryCandidate, target *storage.UpstreamSyncTarget, accountName string) *CandidateDTO {
	t.Helper()
	item, err := svc.Approve(context.Background(), candidate.ID, ApprovalInput{
		TargetID:       target.ID,
		TargetGroupIDs: []int64{101},
		AccountName:    accountName,
		Platform:       "openai",
		Concurrency:    7,
		Weight:         3,
	})
	if err != nil {
		t.Fatalf("approve candidate: %v", err)
	}
	return item
}

func candidateChannelTypes(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	items, err := storage.NewGroupDiscoveryCandidates(db).List()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.SourceChannelName+"/"+item.SourceGroupName] = item.ChannelType
	}
	return out
}

func TestScanClassifiesGroupsIntoChannelBuckets(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{
		{Name: "Plus 主力", Ratio: 1},
		{Name: "Claude Code 专区", Ratio: 1},
		{Name: "Pro 备用", Ratio: 1},
		{Name: "Gemini 2.5", Ratio: 1},
		{Name: "Image 生图", Ratio: 1},
		{Name: "CN DeepSeek", Ratio: 1},
		{Name: "纯文字分组", Ratio: 1},
	}

	result, err := svc.Scan(context.Background(), ScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if result.SelectedCandidates != 7 {
		t.Fatalf("selected candidates = %d, want 7", result.SelectedCandidates)
	}
	got := candidateChannelTypes(t, db)
	want := map[string]string{
		"source/Plus 主力":        sub2pool.ChannelPLUS,
		"source/Claude Code 专区": sub2pool.ChannelCC,
		"source/Pro 备用":         sub2pool.ChannelPro,
		"source/Gemini 2.5":     sub2pool.ChannelGemini,
		"source/Image 生图":       sub2pool.ChannelImage,
		"source/CN DeepSeek":    sub2pool.ChannelCN,
		"source/纯文字分组":          sub2pool.ChannelOther,
	}
	for key, wantType := range want {
		if got[key] != wantType {
			t.Fatalf("channel type for %q = %q, want %q", key, got[key], wantType)
		}
	}
	if sub2pool.ClassifyChannel("Claude Code 专区") != sub2pool.ChannelCC {
		t.Fatalf("sub2pool.ClassifyChannel wrapper does not match pool classification")
	}
}

func TestScanKeepsLowestRatioCandidatesPerChannelWithTies(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	alpha := seedDiscoveryChannel(t, db, "alpha", storage.ChannelTypeSub2API, true)
	beta := seedDiscoveryChannel(t, db, "beta", storage.ChannelTypeSub2API, true)
	channelSvc.groupsByChannel[alpha.ID] = []connector.APIKeyGroup{
		{Name: "plus cheap", Ratio: 0.1},
		{Name: "plus mid a", Ratio: 0.2},
		{Name: "plus mid b", Ratio: 0.2},
		{Name: "plus expensive", Ratio: 0.9},
		{Name: "claude only", Ratio: 5},
	}
	channelSvc.groupsByChannel[beta.ID] = []connector.APIKeyGroup{
		{Name: "plus beta low", Ratio: 0.15},
	}

	result, err := svc.Scan(context.Background(), ScanOptions{TopNPerChannel: 3})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// PLUS is ranked globally across every source channel. The third position
	// has two independent 0.2 groups, so both ties are retained. CC is ranked
	// independently and keeps its only group.
	if result.SelectedCandidates != 5 {
		t.Fatalf("selected candidates = %d, want 5", result.SelectedCandidates)
	}
	items, err := svc.candidates.List()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	kept := make(map[string]float64, len(items))
	for _, item := range items {
		kept[item.SourceGroupName] = item.Ratio
		if item.Status != statusPending {
			t.Fatalf("scanned candidate is not pending: %#v", item)
		}
	}
	want := map[string]float64{
		"plus cheap":    0.1,
		"plus mid a":    0.2,
		"plus mid b":    0.2,
		"claude only":   5,
		"plus beta low": 0.15,
	}
	if len(kept) != len(want) {
		t.Fatalf("kept groups = %v, want %v", kept, want)
	}
	for name, ratio := range want {
		if kept[name] != ratio {
			t.Fatalf("group %q ratio = %v, want %v (kept: %v)", name, kept[name], ratio, kept)
		}
	}
	if _, dropped := kept["plus expensive"]; dropped {
		t.Fatalf("filtered group survived: %v", kept)
	}
}

func TestScanTopNConfigDefaultsAndOverrides(t *testing.T) {
	seed := func(t *testing.T) (*gorm.DB, *Service) {
		db := openDiscoveryTestDB(t)
		channelSvc := newDiscoveryChannelServiceFake()
		svc := newDiscoveryTestService(db, channelSvc)
		source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
		groups := make([]connector.APIKeyGroup, 0, 7)
		for i := 0; i < 7; i++ {
			groups = append(groups, connector.APIKeyGroup{
				Name:  fmt.Sprintf("plus-%02d", i),
				Ratio: float64(i + 1),
			})
		}
		channelSvc.groupsByChannel[source.ID] = groups
		return db, svc
	}

	t.Run("default keeps five", func(t *testing.T) {
		_, svc := seed(t)
		result, err := svc.Scan(context.Background(), ScanOptions{})
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if result.SelectedCandidates != defaultTopNPerChannel {
			t.Fatalf("selected candidates = %d, want default %d", result.SelectedCandidates, defaultTopNPerChannel)
		}
	})

	t.Run("explicit override", func(t *testing.T) {
		_, svc := seed(t)
		result, err := svc.Scan(context.Background(), ScanOptions{TopNPerChannel: 3})
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if result.SelectedCandidates != 3 {
			t.Fatalf("selected candidates = %d, want 3", result.SelectedCandidates)
		}
	})

	t.Run("zero falls back to default", func(t *testing.T) {
		_, svc := seed(t)
		result, err := svc.Scan(context.Background(), ScanOptions{TopNPerChannel: 0})
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if result.SelectedCandidates != defaultTopNPerChannel {
			t.Fatalf("selected candidates = %d, want default %d", result.SelectedCandidates, defaultTopNPerChannel)
		}
	})
}

func TestScanPrunesUnselectedSafeCandidates(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{{Name: "plus old", Ratio: 0.1}}
	if _, err := svc.Scan(context.Background(), ScanOptions{}); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	first, err := svc.candidates.FindBySource(source.ID, sourceGroupKey(connector.APIKeyGroup{Name: "plus old"}))
	if err != nil {
		t.Fatalf("find first candidate: %v", err)
	}
	first.Status = statusRejected
	first.AccountName = "operator value"
	if err := svc.candidates.Update(first); err != nil {
		t.Fatalf("set review state: %v", err)
	}

	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{{Name: "plus new", Ratio: 0.2}}
	result, err := svc.Scan(context.Background(), ScanOptions{})
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if result.SelectedCandidates != 1 {
		t.Fatalf("selected candidates = %d, want 1", result.SelectedCandidates)
	}
	items, err := svc.candidates.List()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].SourceGroupName != "plus new" {
		t.Fatalf("queue was not rebuilt: %#v", items)
	}
	if _, err := svc.candidates.FindByID(first.ID); err != gorm.ErrRecordNotFound {
		t.Fatalf("stale candidate survived rebuild: err=%v", err)
	}
}

func TestScanOnlyReadsSourceGroups(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeNewAPI, true)
	disabled := seedDiscoveryChannel(t, db, "disabled", storage.ChannelTypeSub2API, false)
	groupID := int64(7)
	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{{
		ID:          &groupID,
		Name:        " low-cost ",
		Description: "first scan",
		Ratio:       0.25,
	}}
	channelSvc.groupsByChannel[disabled.ID] = []connector.APIKeyGroup{{Name: "ignored", Ratio: 1}}

	result, err := svc.Scan(context.Background(), ScanOptions{})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if result.TotalChannels != 1 || result.ScannedChannels != 1 || result.SelectedCandidates != 1 {
		t.Fatalf("unexpected scan result: %#v", result)
	}
	if channelSvc.groupCallsByChannel[disabled.ID] != 0 {
		t.Fatalf("disabled channel was scanned %d times", channelSvc.groupCallsByChannel[disabled.ID])
	}
	if channelSvc.listKeyCalls != 0 || channelSvc.createCalls != 0 || channelSvc.updateCalls != 0 || channelSvc.revealCalls != 0 {
		t.Fatalf("scan performed source key operations: list=%d create=%d update=%d reveal=%d", channelSvc.listKeyCalls, channelSvc.createCalls, channelSvc.updateCalls, channelSvc.revealCalls)
	}

	candidate, err := svc.candidates.FindBySource(source.ID, "id:7")
	if err != nil {
		t.Fatalf("find candidate: %v", err)
	}
	if candidate.SourceGroupName != "low-cost" || candidate.Status != statusPending {
		t.Fatalf("stored candidate = %#v", candidate)
	}
	if candidate.ChannelType != sub2pool.ChannelOther {
		t.Fatalf("candidate channel type = %q, want %q", candidate.ChannelType, sub2pool.ChannelOther)
	}
}

func TestScanPreservesReviewedAndCustomCandidateState(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	firstID, secondID := int64(1), int64(2)
	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{
		{ID: &firstID, Name: "plus selected", Ratio: 0.1},
		{ID: &secondID, Name: "plus reviewed", Ratio: 0.2},
	}
	if _, err := svc.Scan(context.Background(), ScanOptions{TopNPerChannel: 2}); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	selected, err := svc.candidates.FindBySource(source.ID, "id:1")
	if err != nil {
		t.Fatalf("find selected candidate: %v", err)
	}
	selected.AccountName = "operator-selected-name"
	if err := svc.candidates.Update(selected); err != nil {
		t.Fatalf("set custom name: %v", err)
	}
	reviewed, err := svc.candidates.FindBySource(source.ID, "id:2")
	if err != nil {
		t.Fatalf("find reviewed candidate: %v", err)
	}
	reviewed.Status = statusApproved
	reviewed.AccountName = "operator-reviewed-name"
	if err := svc.candidates.Update(reviewed); err != nil {
		t.Fatalf("set reviewed state: %v", err)
	}

	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{
		{ID: &firstID, Name: "plus selected renamed", Ratio: 0.05},
		{ID: &secondID, Name: "plus reviewed", Ratio: 0.9},
	}
	result, err := svc.Scan(context.Background(), ScanOptions{TopNPerChannel: 1})
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if result.SelectedCandidates != 1 || result.DeletedCandidates != 0 {
		t.Fatalf("rescan result = %#v", result)
	}
	selected, _ = svc.candidates.FindByID(selected.ID)
	if selected.SourceGroupName != "plus selected renamed" || selected.AccountName != "operator-selected-name" {
		t.Fatalf("selected candidate state = %#v", selected)
	}
	reviewed, err = svc.candidates.FindByID(reviewed.ID)
	if err != nil || reviewed.Status != statusApproved || reviewed.AccountName != "operator-reviewed-name" {
		t.Fatalf("reviewed candidate state = %#v err=%v", reviewed, err)
	}
}

func TestPartialScanDoesNotChangeCandidateQueue(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	working := seedDiscoveryChannel(t, db, "working", storage.ChannelTypeSub2API, true)
	broken := seedDiscoveryChannel(t, db, "broken", storage.ChannelTypeSub2API, true)
	channelSvc.groupsByChannel[working.ID] = []connector.APIKeyGroup{{Name: "plus baseline", Ratio: 0.1}}
	channelSvc.groupsByChannel[broken.ID] = []connector.APIKeyGroup{{Name: "pro baseline", Ratio: 0.2}}
	if _, err := svc.Scan(context.Background(), ScanOptions{}); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	before, err := svc.candidates.List()
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	channelSvc.groupsByChannel[working.ID] = []connector.APIKeyGroup{{Name: "plus replacement", Ratio: 0.01}}
	channelSvc.groupErrorsByChannel[broken.ID] = errors.New("source unavailable")
	result, err := svc.Scan(context.Background(), ScanOptions{})
	if err != nil {
		t.Fatalf("partial scan: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("partial scan result = %#v", result)
	}
	after, err := svc.candidates.List()
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("candidate count changed: before=%#v after=%#v", before, after)
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].SourceGroupName != after[i].SourceGroupName {
			t.Fatalf("partial scan changed queue: before=%#v after=%#v", before, after)
		}
	}
}

func TestScanCreatesUniqueBoundedDefaultNamesAndSafeChannelLinks(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	first := seedDiscoveryChannel(t, db, "first", storage.ChannelTypeSub2API, true)
	second := seedDiscoveryChannel(t, db, "second", storage.ChannelTypeSub2API, true)
	longName := strings.Repeat("分", 120) + " plus"
	channelSvc.groupsByChannel[first.ID] = []connector.APIKeyGroup{{Name: longName, Ratio: 0.1}}
	channelSvc.groupsByChannel[second.ID] = []connector.APIKeyGroup{{Name: longName, Ratio: 0.2}}
	if _, err := svc.Scan(context.Background(), ScanOptions{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	items, err := svc.candidates.List()
	if err != nil || len(items) != 2 {
		t.Fatalf("candidates=%#v err=%v", items, err)
	}
	if items[0].AccountName == items[1].AccountName {
		t.Fatalf("default account names collided: %q", items[0].AccountName)
	}
	for _, item := range items {
		if utf8.RuneCountInString(item.AccountName) > maxTargetAccountNameRunes {
			t.Fatalf("default account name exceeds limit: %q", item.AccountName)
		}
	}
	dtos, err := svc.List()
	if err != nil || len(dtos) != 2 || dtos[0].SourceChannelURL == "" {
		t.Fatalf("candidate links=%#v err=%v", dtos, err)
	}
	if got := safeSourceChannelURL("javascript:alert(1)"); got != "" {
		t.Fatalf("unsafe source channel URL accepted: %q", got)
	}
}

func TestDefaultAccountNameMigrationRequiresUntouchedPendingCandidate(t *testing.T) {
	now := time.Now()
	targetID := uint(3)
	remoteID := int64(7)
	tests := map[string]func(*storage.GroupDiscoveryCandidate){
		"reviewed status":         func(item *storage.GroupDiscoveryCandidate) { item.Status = statusApproved },
		"selected target":         func(item *storage.GroupDiscoveryCandidate) { item.TargetID = &targetID },
		"source key":              func(item *storage.GroupDiscoveryCandidate) { item.SourceAPIKeyID = &remoteID },
		"target account":          func(item *storage.GroupDiscoveryCandidate) { item.TargetAccountID = &remoteID },
		"source creation attempt": func(item *storage.GroupDiscoveryCandidate) { item.SourceKeyCreateAttemptedAt = &now },
		"target creation attempt": func(item *storage.GroupDiscoveryCandidate) { item.TargetAccountCreateAttemptedAt = &now },
		"apply attempt":           func(item *storage.GroupDiscoveryCandidate) { item.LastAttemptAt = &now },
		"applied timestamp":       func(item *storage.GroupDiscoveryCandidate) { item.AppliedAt = &now },
		"operator name":           func(item *storage.GroupDiscoveryCandidate) { item.AccountName = "operator value" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			item := &storage.GroupDiscoveryCandidate{
				Status:          statusPending,
				SourceGroupName: "legacy-default",
				AccountName:     "legacy-default",
			}
			mutate(item)
			if shouldSetDefaultAccountName(item) {
				t.Fatalf("unsafe candidate accepted for migration: %#v", item)
			}
		})
	}
}

func TestScanUpdatesGeneratedDefaultAfterSourceGroupRename(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	groupID := int64(9)
	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{{ID: &groupID, Name: "plus old", Ratio: 0.1}}
	if _, err := svc.Scan(context.Background(), ScanOptions{}); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	item, err := svc.candidates.FindBySource(source.ID, "id:9")
	if err != nil {
		t.Fatalf("find candidate: %v", err)
	}
	if item.AccountName != defaultAccountName("plus old", item.ID) {
		t.Fatalf("initial default name = %q", item.AccountName)
	}
	channelSvc.groupsByChannel[source.ID] = []connector.APIKeyGroup{{ID: &groupID, Name: "plus renamed", Ratio: 0.1}}
	if _, err := svc.Scan(context.Background(), ScanOptions{}); err != nil {
		t.Fatalf("rename scan: %v", err)
	}
	item, err = svc.candidates.FindByID(item.ID)
	if err != nil {
		t.Fatalf("reload candidate: %v", err)
	}
	if item.AccountName != defaultAccountName("plus renamed", item.ID) {
		t.Fatalf("renamed default = %q", item.AccountName)
	}
}
func TestApplyRevalidatesPersistedTargetAccountNameBeforeRemoteWrites(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{Name: "source-low", Ratio: 0.1})
	server, admin := newDiscoveryAdminServer(t)
	defer server.Close()
	target := seedDiscoveryTarget(t, db, server.URL)
	approveDiscoveryCandidate(t, svc, candidate, target, "valid-name")

	stored, err := svc.candidates.FindByID(candidate.ID)
	if err != nil {
		t.Fatalf("reload approved candidate: %v", err)
	}
	stored.AccountName = strings.Repeat("名", maxTargetAccountNameRunes+1)
	if err := svc.candidates.Update(stored); err != nil {
		t.Fatalf("persist legacy overlong name: %v", err)
	}
	sourceGroupCallsBefore := channelSvc.groupCallsByChannel[source.ID]
	groupCallsBefore := admin.groupCalls
	accountListCallsBefore := admin.accountListCalls
	updateCallsBefore := admin.updateCalls
	syncModelCallsBefore := admin.syncModelCalls
	schedulableCallsBefore := len(admin.schedulableCalls)
	result, err := svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Failed != 1 || !strings.Contains(result.Items[0].Error, "too long") {
		t.Fatalf("overlong apply result = %#v", result)
	}
	if channelSvc.groupCallsByChannel[source.ID] != sourceGroupCallsBefore || channelSvc.listKeyCalls != 0 || channelSvc.createCalls != 0 || channelSvc.updateCalls != 0 || channelSvc.revealCalls != 0 ||
		admin.groupCalls != groupCallsBefore || admin.accountListCalls != accountListCallsBefore || admin.createCalls != 0 || admin.updateCalls != updateCallsBefore ||
		admin.syncModelCalls != syncModelCallsBefore || len(admin.schedulableCalls) != schedulableCallsBefore {
		t.Fatalf("overlong name caused remote calls: source_groups=%d keys=%d/%d/%d/%d target_groups=%d accounts=%d/%d/%d models=%d schedulable=%d",
			channelSvc.groupCallsByChannel[source.ID]-sourceGroupCallsBefore, channelSvc.listKeyCalls, channelSvc.createCalls, channelSvc.updateCalls, channelSvc.revealCalls,
			admin.groupCalls-groupCallsBefore, admin.accountListCalls-accountListCallsBefore, admin.createCalls, admin.updateCalls-updateCallsBefore,
			admin.syncModelCalls-syncModelCallsBefore, len(admin.schedulableCalls)-schedulableCallsBefore)
	}
}

func TestApproveEnforcesTargetAccountNameLimit(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{Name: "source-low", Ratio: 0.1})
	server, _ := newDiscoveryAdminServer(t)
	defer server.Close()
	target := seedDiscoveryTarget(t, db, server.URL)

	_, err := svc.Approve(context.Background(), candidate.ID, ApprovalInput{
		TargetID:       target.ID,
		TargetGroupIDs: []int64{101},
		AccountName:    strings.Repeat("名", maxTargetAccountNameRunes+1),
	})
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("approve overlong account name error = %v", err)
	}
}

func TestApproveValidatesTargetGroupsBeforeRecordingMapping(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	sourceGroupID := int64(9)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{ID: &sourceGroupID, Name: "source-low", Ratio: 0.06})
	server, admin := newDiscoveryAdminServer(t)
	defer server.Close()
	target := seedDiscoveryTarget(t, db, server.URL)

	if _, err := svc.Approve(context.Background(), candidate.ID, ApprovalInput{
		TargetID:       target.ID,
		TargetGroupIDs: []int64{102},
	}); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("approve inactive group error = %v, want inactive error", err)
	}
	stored, err := svc.candidates.FindByID(candidate.ID)
	if err != nil {
		t.Fatalf("reload failed approval candidate: %v", err)
	}
	if stored.Status != statusPending || stored.TargetID != nil {
		t.Fatalf("failed approval changed candidate: %#v", stored)
	}

	approved, err := svc.Approve(context.Background(), candidate.ID, ApprovalInput{
		TargetID:       target.ID,
		TargetGroupIDs: []int64{101, 101},
		AccountName:    "mapped-source-low",
		Platform:       "openai",
		Concurrency:    0,
		Weight:         0,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != statusApproved || approved.TargetID == nil || *approved.TargetID != target.ID {
		t.Fatalf("approved candidate = %#v", approved)
	}
	if got := approved.TargetGroupIDs; len(got) != 1 || got[0] != 101 {
		t.Fatalf("approved target groups = %#v", got)
	}
	if approved.AccountName != "mapped-source-low" || approved.Concurrency != 10 || approved.Weight != 1 {
		t.Fatalf("approved defaults = %#v", approved)
	}
	if admin.groupCalls < 2 {
		t.Fatalf("target groups were not read for each approval: %d calls", admin.groupCalls)
	}
	cached, err := storage.NewUpstreamSyncTargetGroups(db).ListByTarget(target.ID, true)
	if err != nil {
		t.Fatalf("list cached target groups: %v", err)
	}
	if len(cached) != 2 || cached[0].RemoteGroupID != 101 || cached[1].RemoteGroupID != 102 {
		t.Fatalf("cached target groups = %#v", cached)
	}
	if channelSvc.createCalls != 0 || admin.createCalls != 0 {
		t.Fatalf("approval created remote objects: source=%d target=%d", channelSvc.createCalls, admin.createCalls)
	}
}

func TestApplyCreatesTrackedObjectsAndDoesNotDuplicateThem(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeNewAPI, true)
	sourceGroupID := int64(12)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{ID: &sourceGroupID, Name: "source-low", Ratio: 0.06})
	server, admin := newDiscoveryAdminServer(t)
	defer server.Close()
	target := seedDiscoveryTarget(t, db, server.URL)
	approveDiscoveryCandidate(t, svc, candidate, target, "discovery-source-low")

	result, err := svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Requested != 1 || result.Applied != 1 || result.Failed != 0 || len(result.Items) != 1 || result.Items[0].Status != statusApplied {
		t.Fatalf("apply result = %#v", result)
	}
	if channelSvc.createCalls != 1 || channelSvc.updateCalls != 1 || channelSvc.revealCalls != 1 {
		t.Fatalf("source key calls = create %d update %d reveal %d", channelSvc.createCalls, channelSvc.updateCalls, channelSvc.revealCalls)
	}
	if channelSvc.lastCreate.UnlimitedQuota == nil || !*channelSvc.lastCreate.UnlimitedQuota || channelSvc.lastCreate.ExpiredTime == nil || *channelSvc.lastCreate.ExpiredTime != -1 {
		t.Fatalf("NewAPI source key defaults = %#v", channelSvc.lastCreate)
	}
	if admin.createCalls != 1 || admin.syncModelCalls != 1 || len(admin.schedulableCalls) != 2 || admin.schedulableCalls[0] || !admin.schedulableCalls[1] {
		t.Fatalf("target account calls = creates %d syncs %d schedulable %#v", admin.createCalls, admin.syncModelCalls, admin.schedulableCalls)
	}

	stored, err := svc.candidates.FindByID(candidate.ID)
	if err != nil {
		t.Fatalf("reload applied candidate: %v", err)
	}
	if stored.Status != statusApplied || stored.SourceAPIKeyID == nil || stored.TargetAccountID == nil || stored.AppliedAt == nil {
		t.Fatalf("stored applied candidate = %#v", stored)
	}
	account := admin.account(*stored.TargetAccountID)
	if account == nil || account["notes"] != discoveryAccountNotes(candidate.ID) || account["status"] != "active" || account["schedulable"] != true {
		t.Fatalf("remote account = %#v", account)
	}
	if account["name"] != "discovery-source-low" || stored.TargetAccountName != "discovery-source-low" {
		t.Fatalf("custom Sub2 account name was not preserved: stored=%q remote=%#v", stored.TargetAccountName, account["name"])
	}
	credentials, ok := account["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("account credentials = %#v", account["credentials"])
	}
	if credentials["api_key"] != "sk-discovery-100" || credentials["base_url"] != source.SiteURL {
		t.Fatalf("account source credentials = %#v", credentials)
	}
	if credentials["pool_mode"] != true || credentials["pool_mode_retry_count"] != float64(0) || credentials["custom_error_codes_enabled"] != true {
		t.Fatalf("account pool-mode credentials = %#v", credentials)
	}
	retryCodes, ok := credentials["pool_mode_retry_status_codes"].([]any)
	if !ok || len(retryCodes) != 0 {
		t.Fatalf("account pool-mode retry codes = %#v", credentials["pool_mode_retry_status_codes"])
	}
	customCodes, ok := credentials["custom_error_codes"].([]any)
	if !ok || len(customCodes) != 2 || customCodes[0] != float64(401) || customCodes[1] != float64(402) {
		t.Fatalf("account custom error codes = %#v", credentials["custom_error_codes"])
	}
	if _, ok := credentials["model_mapping"].(map[string]any); !ok {
		t.Fatalf("model mapping was not written: %#v", credentials)
	}

	result, err = svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if result.Applied != 1 || result.Failed != 0 || channelSvc.createCalls != 1 || admin.createCalls != 1 {
		t.Fatalf("reapply duplicated tracked objects: result=%#v source creates=%d target creates=%d", result, channelSvc.createCalls, admin.createCalls)
	}
}

func TestApplyValidatesCurrentTargetGroupsBeforeCreatingSourceKey(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	sourceGroupID := int64(16)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{ID: &sourceGroupID, Name: "source-low", Ratio: 0.4})
	server, admin := newDiscoveryAdminServer(t)
	defer server.Close()
	target := seedDiscoveryTarget(t, db, server.URL)
	approveDiscoveryCandidate(t, svc, candidate, target, "target-group-changed")

	admin.mu.Lock()
	admin.groups[0]["status"] = "inactive"
	admin.mu.Unlock()
	result, err := svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Applied != 0 || result.Failed != 1 || !strings.Contains(result.Items[0].Error, "not active") {
		t.Fatalf("target group validation result = %#v", result)
	}
	if channelSvc.createCalls != 0 || channelSvc.updateCalls != 0 || channelSvc.revealCalls != 0 || admin.createCalls != 0 {
		t.Fatalf("stale target group caused remote writes: source creates=%d updates=%d reveals=%d target creates=%d", channelSvc.createCalls, channelSvc.updateCalls, channelSvc.revealCalls, admin.createCalls)
	}
}

func TestApplyRetriesPartialFailureUsingRecordedRemoteIDs(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	sourceGroupID := int64(13)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{ID: &sourceGroupID, Name: "source-low", Ratio: 0.2})
	server, admin := newDiscoveryAdminServer(t)
	defer server.Close()
	admin.failModelSyncCount = 1
	target := seedDiscoveryTarget(t, db, server.URL)
	approveDiscoveryCandidate(t, svc, candidate, target, "retry-source-low")

	result, err := svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if result.Applied != 0 || result.Failed != 1 || !strings.Contains(result.Items[0].Error, "sync target account models") {
		t.Fatalf("first apply result = %#v", result)
	}
	failed, err := svc.candidates.FindByID(candidate.ID)
	if err != nil {
		t.Fatalf("reload failed candidate: %v", err)
	}
	if failed.Status != statusFailed || failed.SourceAPIKeyID == nil || failed.TargetAccountID == nil {
		t.Fatalf("partial progress was not recorded: %#v", failed)
	}
	if channelSvc.createCalls != 1 || admin.createCalls != 1 {
		t.Fatalf("first apply creates = source %d target %d", channelSvc.createCalls, admin.createCalls)
	}

	result, err = svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("retry apply: %v", err)
	}
	if result.Applied != 1 || result.Failed != 0 {
		t.Fatalf("retry result = %#v", result)
	}
	if channelSvc.createCalls != 1 || admin.createCalls != 1 || admin.syncModelCalls != 2 {
		t.Fatalf("retry did not reuse objects: source creates=%d target creates=%d sync calls=%d", channelSvc.createCalls, admin.createCalls, admin.syncModelCalls)
	}
	stored, err := svc.candidates.FindByID(candidate.ID)
	if err != nil {
		t.Fatalf("reload retried candidate: %v", err)
	}
	if stored.Status != statusApplied || stored.AppliedAt == nil {
		t.Fatalf("retried candidate = %#v", stored)
	}
}

func TestApplyRefusesToTakeOverManualSourceKey(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	sourceGroupID := int64(14)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{ID: &sourceGroupID, Name: "source-low", Ratio: 1})
	server, admin := newDiscoveryAdminServer(t)
	defer server.Close()
	target := seedDiscoveryTarget(t, db, server.URL)
	approveDiscoveryCandidate(t, svc, candidate, target, "manual-source-key")

	channelSvc.keysByChannel[source.ID] = []connector.APIKey{{
		ID:      77,
		Key:     "manual-key",
		Name:    discoveryAPIKeyName(candidate.ID),
		Group:   "source-low",
		GroupID: &sourceGroupID,
	}}
	result, err := svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Failed != 1 || !strings.Contains(result.Items[0].Error, "name is already occupied") {
		t.Fatalf("manual source collision result = %#v", result)
	}
	if channelSvc.createCalls != 0 || channelSvc.updateCalls != 0 || channelSvc.revealCalls != 0 || admin.createCalls != 0 {
		t.Fatalf("manual source key was touched: source creates=%d updates=%d reveals=%d target creates=%d", channelSvc.createCalls, channelSvc.updateCalls, channelSvc.revealCalls, admin.createCalls)
	}
	stored, err := svc.candidates.FindByID(candidate.ID)
	if err != nil {
		t.Fatalf("reload collision candidate: %v", err)
	}
	if stored.Status != statusFailed || stored.SourceAPIKeyID != nil {
		t.Fatalf("manual source collision state = %#v", stored)
	}
}

func TestApplyRefusesToTakeOverManualTargetAccount(t *testing.T) {
	db := openDiscoveryTestDB(t)
	channelSvc := newDiscoveryChannelServiceFake()
	svc := newDiscoveryTestService(db, channelSvc)
	source := seedDiscoveryChannel(t, db, "source", storage.ChannelTypeSub2API, true)
	sourceGroupID := int64(15)
	candidate := seedDiscoveredGroup(t, svc, source, connector.APIKeyGroup{ID: &sourceGroupID, Name: "source-low", Ratio: 1})
	server, admin := newDiscoveryAdminServer(t)
	defer server.Close()
	target := seedDiscoveryTarget(t, db, server.URL)
	const accountName = "manual-target-account"
	approveDiscoveryCandidate(t, svc, candidate, target, accountName)
	admin.addManualAccount(81, accountName)

	result, err := svc.Apply(context.Background(), []uint{candidate.ID})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Failed != 1 || !strings.Contains(result.Items[0].Error, "unmanaged account") {
		t.Fatalf("manual target collision result = %#v", result)
	}
	if channelSvc.createCalls != 0 || channelSvc.updateCalls != 0 || channelSvc.revealCalls != 0 || admin.createCalls != 0 || admin.updateCalls != 0 {
		t.Fatalf("manual target account caused remote writes: source creates=%d updates=%d reveals=%d target creates=%d target updates=%d", channelSvc.createCalls, channelSvc.updateCalls, channelSvc.revealCalls, admin.createCalls, admin.updateCalls)
	}
	manual := admin.account(81)
	if manual["notes"] != "manually managed" || manual["status"] != "active" {
		t.Fatalf("manual target account was changed: %#v", manual)
	}
	stored, err := svc.candidates.FindByID(candidate.ID)
	if err != nil {
		t.Fatalf("reload collision candidate: %v", err)
	}
	if stored.Status != statusFailed || stored.SourceAPIKeyID != nil || stored.TargetAccountID != nil {
		t.Fatalf("manual target collision state = %#v", stored)
	}
}
