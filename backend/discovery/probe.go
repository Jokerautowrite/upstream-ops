package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/connector/sub2api"
	"github.com/bejix/upstream-ops/backend/storage"
)

const (
	probeStatusOK   = "ok"
	probeStatusFail = "fail"

	probeHTTPTimeout   = 25 * time.Second
	probeBodyLimit     = 1 << 20
	probeMaxModelTries = 3
)

// ProbeResult is returned to the API after a group liveness check.
type ProbeResult struct {
	Candidate CandidateDTO `json:"candidate"`
	OK        bool         `json:"ok"`
	Error     string       `json:"error,omitempty"`
}

// ProbeBatchResult summarizes sequential probes.
type ProbeBatchResult struct {
	Requested int           `json:"requested"`
	OK        int           `json:"ok"`
	Failed    int           `json:"failed"`
	Items     []ProbeResult `json:"items"`
}

// Probe checks whether a discovered source group can actually serve models.
//
// Side effects:
//   - creates or reuses the stable discovery source API key (uo-discovery-key-<id>)
//   - never creates a Sub2 target account
//   - when TargetAccountID already exists, also runs Sub2 admin TestAccount
//
// Success requires listing models and completing one minimal chat/completions call.
func (s *Service) Probe(ctx context.Context, id uint) (*ProbeResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.probeOne(ctx, id)
}

// ProbeMany runs probes sequentially. Empty ids is rejected (never means "all").
func (s *Service) ProbeMany(ctx context.Context, ids []uint) (*ProbeBatchResult, error) {
	if len(ids) == 0 {
		return nil, errors.New("candidate_ids is required")
	}
	out := &ProbeBatchResult{Requested: len(ids), Items: make([]ProbeResult, 0, len(ids))}
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, errors.New("candidate id is invalid")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		s.opMu.Lock()
		item, err := s.probeOne(ctx, id)
		s.opMu.Unlock()
		if err != nil {
			out.Failed++
			out.Items = append(out.Items, ProbeResult{
				Candidate: CandidateDTO{ID: id},
				OK:        false,
				Error:     err.Error(),
			})
			continue
		}
		if item.OK {
			out.OK++
		} else {
			out.Failed++
		}
		out.Items = append(out.Items, *item)
	}
	out.Requested = len(out.Items)
	return out, nil
}

func (s *Service) probeOne(ctx context.Context, id uint) (*ProbeResult, error) {
	started := s.now()
	item, err := s.candidates.FindByID(id)
	if err != nil {
		return nil, err
	}

	status := probeStatusFail
	errText := ""
	detail := ""
	model := ""
	modelCount := 0

	channel, err := s.channels.FindByID(item.SourceChannelID)
	if err != nil {
		errText = fmt.Sprintf("load source channel: %v", err)
	} else {
		sourceGroup, gerr := s.loadSourceGroup(ctx, item)
		if gerr != nil {
			errText = gerr.Error()
		} else {
			item.SourceGroupName = strings.TrimSpace(sourceGroup.Name)
			item.SourceGroupDescription = strings.TrimSpace(sourceGroup.Description)
			item.Ratio = sourceGroup.Ratio
			_ = s.candidates.Update(item)

			_, secret, kerr := s.ensureSourceAPIKey(ctx, item, channel, sourceGroup)
			// reload after ensure may have written key ids
			if refreshed, rerr := s.candidates.FindByID(item.ID); rerr == nil {
				item = refreshed
			}
			if kerr != nil {
				errText = kerr.Error()
			} else {
				base := strings.TrimSpace(channel.SiteURL)
				platform := strings.TrimSpace(item.Platform)
				if platform == "" {
					platform = "openai"
				}
				models, merr := probeListModels(ctx, base, platform, secret)
				if merr != nil {
					errText = merr.Error()
				} else {
					modelCount = len(models)
					picked, perr := probeChat(ctx, base, platform, secret, models, item.ChannelType)
					if perr != nil {
						errText = perr.Error()
						if modelCount > 0 {
							detail = fmt.Sprintf("models=%d chat_failed", modelCount)
						}
					} else {
						model = picked
						status = probeStatusOK
						detail = fmt.Sprintf("models=%d chat_ok model=%s", modelCount, model)
					}
				}

				// Applied accounts: also exercise Sub2 admin test when possible.
				if status == probeStatusOK && item.TargetAccountID != nil && item.TargetID != nil {
					if subErr := s.probeSub2Account(ctx, item, model); subErr != nil {
						status = probeStatusFail
						errText = "source ok, sub2 test failed: " + subErr.Error()
						detail = strings.TrimSpace(detail + " sub2_fail")
					} else {
						detail = strings.TrimSpace(detail + " sub2_ok")
					}
				}
			}
		}
	}

	latency := int(s.now().Sub(started).Milliseconds())
	if latency < 0 {
		latency = 0
	}
	probedAt := s.now()
	if err := s.candidates.SetProbeResult(item.ID, status, errText, detail, model, modelCount, latency, &probedAt); err != nil {
		return nil, err
	}
	item.ProbeStatus = status
	item.ProbeError = errText
	item.ProbeDetail = detail
	item.ProbeModel = model
	item.ProbeModelCount = modelCount
	item.ProbeLatencyMs = latency
	item.ProbedAt = &probedAt

	dto, err := s.toDTO(item)
	if err != nil {
		return nil, err
	}
	return &ProbeResult{
		Candidate: dto,
		OK:        status == probeStatusOK,
		Error:     errText,
	}, nil
}

func (s *Service) probeSub2Account(ctx context.Context, item *storage.GroupDiscoveryCandidate, preferredModel string) error {
	if item.TargetID == nil || item.TargetAccountID == nil {
		return nil
	}
	_, adminTarget, err := s.loadTarget(*item.TargetID)
	if err != nil {
		return err
	}
	client := sub2api.NewAdminClient()
	model := strings.TrimSpace(preferredModel)
	if model == "" {
		models, lerr := client.ListAccountModels(ctx, adminTarget, *item.TargetAccountID)
		if lerr != nil {
			return lerr
		}
		if len(models) == 0 {
			return errors.New("sub2 account has no models")
		}
		model = models[0]
	}
	_, err = client.TestAccount(ctx, adminTarget, *item.TargetAccountID, model)
	return err
}

func probeListModels(ctx context.Context, baseURL, platform, apiKey string) ([]string, error) {
	endpoint := probeModelsURL(baseURL, platform)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	setProbeAuthHeaders(req, platform, apiKey)
	resp, err := (&http.Client{Timeout: probeHTTPTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > probeBodyLimit {
		return nil, errors.New("model list response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list models HTTP %d: %s", resp.StatusCode, truncateProbe(string(body), 240))
	}
	models, err := decodeProbeModels(body)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, errors.New("model list is empty")
	}
	return models, nil
}

func probeChat(ctx context.Context, baseURL, platform, apiKey string, models []string, channelType string) (string, error) {
	ordered := preferProbeModels(models, channelType)
	if len(ordered) == 0 {
		return "", errors.New("no model candidates for chat probe")
	}
	if len(ordered) > probeMaxModelTries {
		ordered = ordered[:probeMaxModelTries]
	}
	var lastErr error
	for _, model := range ordered {
		if err := probeChatOnce(ctx, baseURL, platform, apiKey, model); err != nil {
			lastErr = fmt.Errorf("%s: %w", model, err)
			continue
		}
		return model, nil
	}
	if lastErr == nil {
		lastErr = errors.New("chat probe failed")
	}
	return "", lastErr
}

func probeChatOnce(ctx context.Context, baseURL, platform, apiKey, model string) error {
	endpoint := probeChatURL(baseURL, platform)
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens":  8,
		"temperature": 0,
		"stream":      false,
	}
	// Anthropic-compatible gateways often expect max_tokens only + different body;
	// keep OpenAI-compatible first (covers NewAPI/Sub2API sources we manage).
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	setProbeAuthHeaders(req, platform, apiKey)
	resp, err := (&http.Client{Timeout: probeHTTPTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit+1))
	if err != nil {
		return err
	}
	if len(body) > probeBodyLimit {
		return errors.New("chat response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateProbe(string(body), 240))
	}
	// Accept any JSON object with choices or content — enough to prove the group routes.
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("invalid chat json: %w", err)
	}
	if _, ok := parsed["error"]; ok {
		return fmt.Errorf("chat error payload: %s", truncateProbe(string(body), 240))
	}
	if choices, ok := parsed["choices"]; ok {
		if arr, ok := choices.([]any); ok && len(arr) > 0 {
			return nil
		}
	}
	if content, ok := parsed["content"]; ok && content != nil {
		return nil
	}
	// Some gateways return only id/object without choices for tiny probes — still HTTP 200.
	if id, ok := parsed["id"].(string); ok && strings.TrimSpace(id) != "" {
		return nil
	}
	return errors.New("chat response missing choices")
}

func preferProbeModels(models []string, channelType string) []string {
	prefs := preferredModelHints(channelType)
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, pref := range prefs {
		pref = strings.ToLower(pref)
		for _, model := range models {
			m := strings.TrimSpace(model)
			if m == "" {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			if strings.Contains(strings.ToLower(m), pref) {
				seen[m] = struct{}{}
				out = append(out, m)
			}
		}
	}
	for _, model := range models {
		m := strings.TrimSpace(model)
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

func preferredModelHints(channelType string) []string {
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "plus", "cc", "pro", "chatgpt", "openai":
		return []string{"gpt-4o-mini", "gpt-4o", "gpt-3.5", "chatgpt", "gpt"}
	case "claude", "kimi", "anthropic":
		return []string{"claude", "sonnet", "haiku", "kimi"}
	case "gemini", "google":
		return []string{"gemini", "flash", "pro"}
	case "image":
		return []string{"gpt-image", "dall-e", "flux", "image", "gpt-4o"}
	case "grok":
		return []string{"grok"}
	default:
		return []string{"mini", "flash", "haiku", "gpt", "claude", "gemini"}
	}
}

func probeModelsURL(base, platform string) string {
	normalized := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.EqualFold(strings.TrimSpace(platform), "gemini") {
		if strings.HasSuffix(normalized, "/v1beta/models") {
			return normalized
		}
		if strings.HasSuffix(normalized, "/v1beta") {
			return normalized + "/models"
		}
		return normalized + "/v1beta/models"
	}
	if strings.HasSuffix(normalized, "/v1/models") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/v1") {
		return normalized + "/models"
	}
	return normalized + "/v1/models"
}

func probeChatURL(base, platform string) string {
	normalized := strings.TrimRight(strings.TrimSpace(base), "/")
	_ = platform
	if strings.HasSuffix(normalized, "/v1/chat/completions") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/v1") {
		return normalized + "/chat/completions"
	}
	if strings.HasSuffix(normalized, "/chat/completions") {
		return normalized
	}
	return normalized + "/v1/chat/completions"
}

func setProbeAuthHeaders(req *http.Request, platform, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "gemini":
		req.Header.Set("x-goog-api-key", apiKey)
	case "anthropic", "antigravity":
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func decodeProbeModels(body []byte) ([]string, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return uniqueProbeStrings(collectProbeModelIDs(raw)), nil
}

func collectProbeModelIDs(raw any) []string {
	switch value := raw.(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, collectProbeModelIDs(item)...)
		}
		return out
	case map[string]any:
		out := []string(nil)
		if models, ok := value["models"]; ok {
			out = append(out, collectProbeModelIDs(models)...)
		}
		if data, ok := value["data"]; ok {
			out = append(out, collectProbeModelIDs(data)...)
		}
		for _, key := range []string{"id", "name", "model"} {
			if text, ok := value[key].(string); ok {
				out = append(out, text)
				break
			}
		}
		return out
	case string:
		return []string{value}
	default:
		return nil
	}
}

func uniqueProbeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func truncateProbe(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
