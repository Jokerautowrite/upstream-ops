package sub2api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PoolAccount keeps the raw Admin API account only inside backend code. Callers
// must use Identity rather than serializing Account.Credentials.
type PoolAccount struct {
	Account           AdminAccount
	APIKeyFingerprint string
	Health            PoolAccountHealth
	Stats             PoolAccountStats
}

// PoolAccountHealth contains display-safe account health fields. It deliberately
// excludes raw error messages and credentials.
type PoolAccountHealth struct {
	CurrentConcurrency       int
	RateLimited              bool
	TemporarilyUnschedulable bool
	Overloaded               bool
}

// PoolAccountStats contains safe display-only daily aggregate values.
type PoolAccountStats struct {
	TodayRequests *int
	TodayCost     *float64
}

type PoolTodayStats struct {
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
}

// PoolAccountIdentity intentionally contains only a normalized identity
// derivative. The API key is hashed before this value leaves the connector.
type PoolAccountIdentity struct {
	BaseURL         string
	APIKeySHA256    string
	FingerprintSeen bool
}

// Identity derives a full API-key fingerprint from credentials when available.
// Newer Sub2 versions may return a precomputed fingerprint instead; those
// compatibility fields are accepted only when they are a complete SHA-256 hex.
func (a PoolAccount) Identity() PoolAccountIdentity {
	baseURL := credentialString(a.Account.Credentials, "base_url", "url")
	rawKey := credentialString(a.Account.Credentials, "api_key", "key")
	if rawKey != "" {
		sum := sha256.Sum256([]byte(rawKey))
		return PoolAccountIdentity{
			BaseURL:         baseURL,
			APIKeySHA256:    hex.EncodeToString(sum[:]),
			FingerprintSeen: true,
		}
	}
	if fingerprint := normalizeSHA256(a.APIKeyFingerprint); fingerprint != "" {
		return PoolAccountIdentity{
			BaseURL:         baseURL,
			APIKeySHA256:    fingerprint,
			FingerprintSeen: true,
		}
	}
	if fingerprint := credentialString(a.Account.Credentials,
		"api_key_sha256",
		"api_key_fingerprint",
		"api_key_hash",
		"_apiKeySha256",
	); normalizeSHA256(fingerprint) != "" {
		return PoolAccountIdentity{
			BaseURL:         baseURL,
			APIKeySHA256:    normalizeSHA256(fingerprint),
			FingerprintSeen: true,
		}
	}
	return PoolAccountIdentity{BaseURL: baseURL}
}

func credentialString(values map[string]any, names ...string) string {
	for _, name := range names {
		value, ok := values[name]
		if !ok {
			continue
		}
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func normalizeSHA256(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return ""
	}
	for _, ch := range value {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return ""
		}
	}
	return value
}

// ListPoolAccounts reads one page of pool accounts without changing any
// account. It accepts both the current object response and the older array
// response used by some Sub2 versions.
func (a *AdminClient) ListPoolAccounts(ctx context.Context, t AdminTarget, page, pageSize int) ([]PoolAccount, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	params := url.Values{}
	params.Set("page", strconv.Itoa(page))
	params.Set("page_size", strconv.Itoa(pageSize))
	body, err := a.getJSON(ctx, t, "/api/v1/admin/accounts?"+params.Encode())
	if err != nil {
		return nil, err
	}
	return decodePoolAccountList(body)
}

// ListAllPoolAccounts fetches pages until the Admin API reports a short page.
// The fixed upper bound prevents a buggy remote pagination implementation from
// causing an unbounded request loop.
func (a *AdminClient) ListAllPoolAccounts(ctx context.Context, t AdminTarget) ([]PoolAccount, error) {
	const pageSize = 100
	const maxPages = 1000

	var all []PoolAccount
	for page := 1; page <= maxPages; page++ {
		items, err := a.ListPoolAccounts(ctx, t, page, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < pageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("admin account pagination exceeded %d pages", maxPages)
}

func (a *AdminClient) GetPoolAccount(ctx context.Context, t AdminTarget, id int64) (*PoolAccount, error) {
	body, err := a.getJSON(ctx, t, "/api/v1/admin/accounts/"+strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	account, err := decodePoolAccount(body)
	if err != nil {
		return nil, err
	}
	return &account, nil
}

func (a *AdminClient) GetPoolTodayStatsBatch(ctx context.Context, t AdminTarget, accountIDs []int64) (map[int64]PoolTodayStats, error) {
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("x-api-key", t.APIKey).
		SetBody(map[string][]int64{"account_ids": accountIDs}).
		Post(strings.TrimRight(t.BaseURL, "/") + "/api/v1/admin/accounts/today-stats/batch")
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, fmt.Errorf("pool today stats status %d", resp.StatusCode())
	}
	var wrapped struct {
		Code int `json:"code"`
		Data struct {
			Stats map[string]PoolTodayStats `json:"stats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body(), &wrapped); err != nil {
		return nil, fmt.Errorf("decode pool today stats: %w", err)
	}
	if wrapped.Code != 0 {
		return nil, errors.New("pool today stats rejected")
	}
	out := make(map[int64]PoolTodayStats, len(wrapped.Data.Stats))
	for rawID, stats := range wrapped.Data.Stats {
		accountID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || accountID <= 0 {
			continue
		}
		out[accountID] = stats
	}
	return out, nil
}

// UpdatePoolAccountPriority prefers the dedicated PATCH endpoint. Older Sub2
// versions only accept the legacy priority-only PUT, which is used solely for
// compatibility after an explicit unsupported-endpoint response.
func (a *AdminClient) UpdatePoolAccountPriority(ctx context.Context, t AdminTarget, id int64, priority int) (*PoolAccount, error) {
	account, status, err := a.writePoolAccount(ctx, t, http.MethodPatch,
		"/api/v1/admin/accounts/"+strconv.FormatInt(id, 10)+"/priority",
		map[string]int{"priority": priority},
	)
	if err == nil {
		return account, nil
	}
	if !compatibilityStatus(status) {
		return nil, err
	}
	account, _, err = a.writePoolAccount(ctx, t, http.MethodPut,
		"/api/v1/admin/accounts/"+strconv.FormatInt(id, 10),
		map[string]int{"priority": priority},
	)
	return account, err
}

// SetPoolAccountSchedulable follows the same PATCH-first compatibility policy
// as priority writes.
func (a *AdminClient) SetPoolAccountSchedulable(ctx context.Context, t AdminTarget, id int64, schedulable bool) (*PoolAccount, error) {
	account, status, err := a.writePoolAccount(ctx, t, http.MethodPatch,
		"/api/v1/admin/accounts/"+strconv.FormatInt(id, 10)+"/schedulable",
		map[string]bool{"schedulable": schedulable},
	)
	if err == nil {
		return account, nil
	}
	if !compatibilityStatus(status) {
		return nil, err
	}
	account, _, err = a.writePoolAccount(ctx, t, http.MethodPost,
		"/api/v1/admin/accounts/"+strconv.FormatInt(id, 10)+"/schedulable",
		map[string]bool{"schedulable": schedulable},
	)
	return account, err
}

func (a *AdminClient) writePoolAccount(ctx context.Context, t AdminTarget, method, path string, body any) (*PoolAccount, int, error) {
	resp, err := a.client.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("x-api-key", t.APIKey).
		SetBody(body).
		Execute(method, strings.TrimRight(t.BaseURL, "/")+path)
	if err != nil {
		return nil, 0, err
	}
	if resp.IsError() {
		return nil, resp.StatusCode(), fmt.Errorf("pool account write status %d", resp.StatusCode())
	}
	account, err := decodePoolAccountResponse(resp.Body())
	if err != nil {
		return nil, resp.StatusCode(), err
	}
	return &account, resp.StatusCode(), nil
}

func compatibilityStatus(status int) bool {
	return status == http.StatusNotFound ||
		status == http.StatusMethodNotAllowed ||
		status == http.StatusNotImplemented
}

func decodePoolAccountList(body []byte) ([]PoolAccount, error) {
	var wrapped struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Items != nil {
		return decodePoolAccounts(wrapped.Items)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode pool accounts: %w", err)
	}
	return decodePoolAccounts(raw)
}

func decodePoolAccounts(raw []json.RawMessage) ([]PoolAccount, error) {
	out := make([]PoolAccount, 0, len(raw))
	for _, item := range raw {
		account, err := decodePoolAccount(item)
		if err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	return out, nil
}

func decodePoolAccountResponse(body []byte) (PoolAccount, error) {
	var wrapped struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Data != nil {
		if wrapped.Code != 0 {
			return PoolAccount{}, fmt.Errorf("pool account update rejected")
		}
		return decodePoolAccount(wrapped.Data)
	}
	return decodePoolAccount(body)
}

func decodePoolAccount(raw []byte) (PoolAccount, error) {
	var account AdminAccount
	if err := json.Unmarshal(raw, &account); err != nil {
		return PoolAccount{}, fmt.Errorf("decode pool account: %w", err)
	}
	var compatibility map[string]json.RawMessage
	if err := json.Unmarshal(raw, &compatibility); err != nil {
		return PoolAccount{}, fmt.Errorf("decode pool account compatibility fields: %w", err)
	}
	fingerprint := ""
	for _, field := range []string{
		"api_key_sha256",
		"api_key_fingerprint",
		"api_key_hash",
		"_apiKeySha256",
		"apiKeySha256",
	} {
		value, ok := compatibility[field]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			fingerprint = normalizeSHA256(text)
		}
		if fingerprint != "" {
			break
		}
	}
	var nested struct {
		CredentialFingerprint struct {
			APIKeySHA256 string `json:"api_key_sha256"`
		} `json:"credential_fingerprint"`
		CurrentConcurrency     int             `json:"current_concurrency"`
		RateLimited            json.RawMessage `json:"rate_limited"`
		RateLimitResetAt       json.RawMessage `json:"rate_limit_reset_at"`
		TempUnschedulable      json.RawMessage `json:"temp_unschedulable"`
		TempUnschedulableUntil json.RawMessage `json:"temp_unschedulable_until"`
		Overloaded             json.RawMessage `json:"overloaded"`
		OverloadUntil          json.RawMessage `json:"overload_until"`
		TodayRequests          json.RawMessage `json:"today_requests"`
		TodayCost              json.RawMessage `json:"today_cost"`
		TodayActualCost        json.RawMessage `json:"today_actual_cost"`
	}
	if err := json.Unmarshal(raw, &nested); err != nil {
		return PoolAccount{}, fmt.Errorf("decode pool account health fields: %w", err)
	}
	if fingerprint == "" {
		fingerprint = normalizeSHA256(nested.CredentialFingerprint.APIKeySHA256)
	}
	return PoolAccount{
		Account:           account,
		APIKeyFingerprint: fingerprint,
		Health: PoolAccountHealth{
			CurrentConcurrency: nested.CurrentConcurrency,
			RateLimited:        boolJSON(nested.RateLimited) || futureTimeJSON(nested.RateLimitResetAt),
			TemporarilyUnschedulable: boolJSON(nested.TempUnschedulable) ||
				futureTimeJSON(nested.TempUnschedulableUntil),
			Overloaded: boolJSON(nested.Overloaded) || futureTimeJSON(nested.OverloadUntil),
		},
		Stats: PoolAccountStats{
			TodayRequests: integerJSON(nested.TodayRequests),
			TodayCost:     firstFloatJSON(nested.TodayCost, nested.TodayActualCost),
		},
	}, nil
}

func boolJSON(value json.RawMessage) bool {
	text := strings.TrimSpace(string(value))
	if text == "" || text == "null" || text == `""` || text == "0" {
		return false
	}
	var flag bool
	if err := json.Unmarshal(value, &flag); err == nil {
		return flag
	}
	return false
}

func futureTimeJSON(value json.RawMessage) bool {
	text := strings.TrimSpace(string(value))
	if text == "" || text == "null" || text == `""` {
		return false
	}
	var timestamp time.Time
	if err := json.Unmarshal(value, &timestamp); err == nil {
		return timestamp.After(time.Now())
	}
	var unix int64
	if err := json.Unmarshal(value, &unix); err == nil {
		return time.Unix(unix, 0).After(time.Now())
	}
	return false
}

func integerJSON(value json.RawMessage) *int {
	text := strings.TrimSpace(string(value))
	if text == "" || text == "null" || text == `""` {
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return nil
	}
	parsed, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || parsed < 0 || parsed != float64(int(parsed)) {
		return nil
	}
	result := int(parsed)
	return &result
}

func firstFloatJSON(values ...json.RawMessage) *float64 {
	for _, value := range values {
		text := strings.TrimSpace(string(value))
		if text == "" || text == "null" || text == `""` {
			continue
		}
		var number json.Number
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&number); err != nil {
			continue
		}
		parsed, err := strconv.ParseFloat(number.String(), 64)
		if err != nil {
			continue
		}
		return &parsed
	}
	return nil
}
