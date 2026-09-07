package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"equipment-telemetry-simulator/internal/model"
)

var toirEquipmentID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type PushClient struct {
	targetURL    string
	secret       string
	httpClient   *http.Client
	maxRetries   int
	retryBackoff time.Duration
}

type IngestResult struct {
	Accepted int `json:"accepted"`
	Skipped  int `json:"skipped"`
	Rejected int `json:"rejected"`
}

func NewPushClient(targetURL, secret string) *PushClient {
	return &PushClient{
		targetURL: targetURL,
		secret:    secret,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		maxRetries:   3,
		retryBackoff: time.Second,
	}
}

func canonicalToirEquipmentID(assetID string) (string, bool) {
	id := strings.TrimSpace(assetID)
	lower := strings.ToLower(id)
	switch {
	case strings.HasPrefix(lower, "eq-"):
		id = id[3:]
	case strings.HasPrefix(lower, "toir-"):
		id = id[5:]
	}
	if !toirEquipmentID.MatchString(id) {
		return "", false
	}
	return id, true
}

func FilterToirEquipment(assets []model.Asset) []model.Asset {
	filtered := make([]model.Asset, 0, len(assets))
	for _, asset := range assets {
		if id, ok := canonicalToirEquipmentID(asset.AssetID); ok {
			asset.AssetID = id
			filtered = append(filtered, asset)
		}
	}
	return filtered
}

func (c *PushClient) Push(ctx context.Context, assets []model.Asset) (*IngestResult, error) {
	toirAssets := FilterToirEquipment(assets)
	if len(toirAssets) == 0 {
		return &IngestResult{}, nil
	}

	payload := struct {
		SentAt time.Time     `json:"sentAt"`
		Assets []model.Asset `json:"assets"`
	}{
		SentAt: time.Now().UTC(),
		Assets: toirAssets,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal push payload: %w", err)
	}

	retries := c.maxRetries
	if retries < 1 {
		retries = 1
	}

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		result, status, err := c.pushOnce(ctx, body)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !shouldRetryPush(status, err) || attempt == retries {
			return nil, lastErr
		}
		if err := waitRetry(ctx, c.retryBackoff, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *PushClient) pushOnce(ctx context.Context, body []byte) (*IngestResult, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.secret != "" {
		req.Header.Set("X-Toir-Telemetry-Secret", c.secret)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("send push request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, resp.StatusCode, fmt.Errorf("push target returned %s: %s", resp.Status, string(raw))
	}

	var result IngestResult
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	return &result, resp.StatusCode, nil
}

func shouldRetryPush(status int, err error) bool {
	if status == 0 {
		return err != nil
	}
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func waitRetry(ctx context.Context, backoff time.Duration, attempt int) error {
	if backoff <= 0 {
		return nil
	}
	timer := time.NewTimer(backoff * time.Duration(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
