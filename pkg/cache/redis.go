package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RedisCache provides a lightweight client for Upstash Redis REST API
type RedisCache struct {
	restURL string
	token   string
	client  *http.Client
}

// NewRedisCache creates a new Redis client using Upstash REST API
func NewRedisCache(restURL, token string) *RedisCache {
	restURL = strings.TrimRight(restURL, "/")
	return &RedisCache{
		restURL: restURL,
		token:   token,
		client:  &http.Client{Timeout: 4 * time.Second},
	}
}

// IsConfigured returns true if Upstash credentials are set
func (r *RedisCache) IsConfigured() bool {
	return r != nil && r.restURL != "" && r.token != ""
}

// Get fetches a string value by key
func (r *RedisCache) Get(ctx context.Context, key string) (string, error) {
	if !r.IsConfigured() {
		return "", nil
	}

	cmd := []any{"GET", key}
	body, err := json.Marshal(cmd)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", r.restURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upstash error: status %d", resp.StatusCode)
	}

	var res struct {
		Result *string `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if res.Result == nil {
		return "", nil // cache miss
	}
	return *res.Result, nil
}

// Set stores a key-value pair with an expiration TTL (in seconds)
func (r *RedisCache) Set(ctx context.Context, key, value string, ttlSeconds int) error {
	if !r.IsConfigured() {
		return nil
	}

	var cmd []any
	if ttlSeconds > 0 {
		cmd = []any{"SET", key, value, "EX", ttlSeconds}
	} else {
		cmd = []any{"SET", key, value}
	}

	body, err := json.Marshal(cmd)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", r.restURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// GetJSON retrieves and deserializes cached JSON
func (r *RedisCache) GetJSON(ctx context.Context, key string, target any) (bool, error) {
	val, err := r.Get(ctx, key)
	if err != nil || val == "" {
		return false, err
	}
	if err := json.Unmarshal([]byte(val), target); err != nil {
		return false, err
	}
	return true, nil
}

// SetJSON serializes and caches an object as JSON
func (r *RedisCache) SetJSON(ctx context.Context, key string, value any, ttlSeconds int) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.Set(ctx, key, string(data), ttlSeconds)
}

// Incr increments a numeric key and returns the updated count
func (r *RedisCache) Incr(ctx context.Context, key string) (int, error) {
	if !r.IsConfigured() {
		return 1, nil
	}

	cmd := []any{"INCR", key}
	body, err := json.Marshal(cmd)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", r.restURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var res struct {
		Result int `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, err
	}
	return res.Result, nil
}

// GetInt fetches an integer value by key, returning 0 if not set
func (r *RedisCache) GetInt(ctx context.Context, key string) (int, error) {
	val, err := r.Get(ctx, key)
	if err != nil || val == "" {
		return 0, err
	}
	var num int
	if _, err := fmt.Sscanf(val, "%d", &num); err != nil {
		return 0, nil
	}
	return num, nil
}

