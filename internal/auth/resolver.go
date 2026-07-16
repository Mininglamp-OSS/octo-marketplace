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

type Resolver interface {
	Resolve(ctx context.Context, token string) (model.Identity, error)
}

type HTTPResolver struct {
	baseURL string
	client  *http.Client
}

func NewHTTPResolver(baseURL string) *HTTPResolver {
	return &HTTPResolver{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (r *HTTPResolver) Resolve(ctx context.Context, token string) (model.Identity, error) {
	if token == "" {
		return model.Identity{}, nil
	}
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return model.Identity{}, fmt.Errorf("encode verify request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/auth/verify?include=context", bytes.NewReader(body))
	if err != nil {
		return model.Identity{}, fmt.Errorf("create verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return model.Identity{}, fmt.Errorf("verify token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return model.Identity{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return model.Identity{}, fmt.Errorf("verify token returned status %d", resp.StatusCode)
	}
	var identity model.Identity
	if err := json.NewDecoder(resp.Body).Decode(&identity); err != nil {
		return model.Identity{}, fmt.Errorf("decode verify response: %w", err)
	}
	return identity, nil
}
