package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

type BotResolver interface {
	ResolveBot(ctx context.Context, token string) (model.BotIdentity, error)
}

type HTTPBotResolver struct {
	baseURL string
	client  *http.Client
}

func NewHTTPBotResolver(baseURL string) *HTTPBotResolver {
	return &HTTPBotResolver{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (r *HTTPBotResolver) ResolveBot(ctx context.Context, token string) (model.BotIdentity, error) {
	if token == "" {
		return model.BotIdentity{}, nil
	}
	body, err := json.Marshal(map[string]string{"bot_token": token})
	if err != nil {
		return model.BotIdentity{}, fmt.Errorf("encode bot verify request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/auth/verify-bot", bytes.NewReader(body))
	if err != nil {
		return model.BotIdentity{}, fmt.Errorf("create bot verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return model.BotIdentity{}, fmt.Errorf("verify bot token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return model.BotIdentity{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return model.BotIdentity{}, fmt.Errorf("verify bot token returned status %d", resp.StatusCode)
	}
	var identity model.BotIdentity
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		return model.BotIdentity{}, fmt.Errorf("decode bot verify response: %w", err)
	}
	return identity, nil
}
