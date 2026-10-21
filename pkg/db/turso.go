package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TursoDB manages marketing, analytics, and object lifecycle tracking in Turso (libSQL)
type TursoDB struct {
	pipelineURL string
	authToken   string
	client      *http.Client
}

// NewTursoDB creates a new Turso database manager
func NewTursoDB(dbURL, authToken string) *TursoDB {
	dbURL = strings.TrimRight(dbURL, "/")
	dbURL = strings.Replace(dbURL, "libsql://", "https://", 1)
	if !strings.HasPrefix(dbURL, "http://") && !strings.HasPrefix(dbURL, "https://") && dbURL != "" {
		dbURL = "https://" + dbURL
	}
	pipelineURL := dbURL + "/v2/pipeline"

	return &TursoDB{
		pipelineURL: pipelineURL,
		authToken:   authToken,
		client:      &http.Client{Timeout: 8 * time.Second},
	}
}

// IsConfigured returns true if Turso credentials are provided
func (t *TursoDB) IsConfigured() bool {
	return t != nil && t.pipelineURL != "" && t.authToken != ""
}

// InitSchema ensures the torrents and cloud_objects tables exist
func (t *TursoDB) InitSchema(ctx context.Context) error {
	if !t.IsConfigured() {
		return nil
	}

	createTorrents := `CREATE TABLE IF NOT EXISTS torrents (
		id TEXT PRIMARY KEY,
		info_hash TEXT NOT NULL UNIQUE,
		magnet_uri TEXT,
		name TEXT,
		total_length INTEGER,
		formatted_size TEXT,
		file_count INTEGER,
		client_ip TEXT,
		user_agent TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`

	createObjects := `CREATE TABLE IF NOT EXISTS cloud_objects (
		id TEXT PRIMARY KEY,
		info_hash TEXT NOT NULL,
		file_index INTEGER NOT NULL,
		file_name TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		bucket TEXT NOT NULL,
		s3_key TEXT NOT NULL,
		download_url TEXT,
		status TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		expires_at TIMESTAMP NOT NULL,
		purged_at TIMESTAMP
	);`

	if err := t.execute(ctx, createTorrents); err != nil {
		return fmt.Errorf("failed to init torrents table: %w", err)
	}
	if err := t.execute(ctx, createObjects); err != nil {
		return fmt.Errorf("failed to init cloud_objects table: %w", err)
	}

	fmt.Println("📊 [Turso Database] Schema initialized successfully.")
	return nil
}

func (t *TursoDB) execute(ctx context.Context, sql string, args ...any) error {
	if !t.IsConfigured() {
		return nil
	}

	params := make([]map[string]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			params[i] = map[string]any{"type": "text", "value": v}
		case int:
			params[i] = map[string]any{"type": "integer", "value": fmt.Sprintf("%d", v)}
		case int64:
			params[i] = map[string]any{"type": "integer", "value": fmt.Sprintf("%d", v)}
		case nil:
			params[i] = map[string]any{"type": "null"}
		default:
			params[i] = map[string]any{"type": "text", "value": fmt.Sprintf("%v", v)}
		}
	}

	stmt := map[string]any{
		"sql": sql,
	}
	if len(params) > 0 {
		stmt["args"] = params
	}

	payload := map[string]any{
		"requests": []map[string]any{
			{
				"type": "execute",
				"stmt": stmt,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", t.pipelineURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("turso query error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// RecordTorrent stores resolved magnet metadata for analytics & marketing
func (t *TursoDB) RecordTorrent(ctx context.Context, infoHash, magnetURI, name string, totalLength int64, formattedSize string, fileCount int, clientIP, userAgent string) {
	if !t.IsConfigured() {
		return
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		query := `INSERT INTO torrents (id, info_hash, magnet_uri, name, total_length, formatted_size, file_count, client_ip, user_agent)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(info_hash) DO UPDATE SET
				name = excluded.name,
				total_length = excluded.total_length,
				formatted_size = excluded.formatted_size,
				file_count = excluded.file_count;`

		id := uuid.New().String()
		_ = t.execute(bgCtx, query, id, infoHash, magnetURI, name, totalLength, formattedSize, fileCount, clientIP, userAgent)
	}()
}

// RecordCloudObject stores metadata of uploaded cloud files
func (t *TursoDB) RecordCloudObject(ctx context.Context, id, infoHash string, fileIndex int, fileName string, sizeBytes int64, bucket, s3Key, downloadURL string, expiresAt time.Time) {
	if !t.IsConfigured() {
		return
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		query := `INSERT INTO cloud_objects (id, info_hash, file_index, file_name, size_bytes, bucket, s3_key, download_url, status, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?);`

		_ = t.execute(bgCtx, query, id, infoHash, fileIndex, fileName, sizeBytes, bucket, s3Key, downloadURL, expiresAt.Format(time.RFC3339))
	}()
}

// ExpireCloudObject marks object as expired and flushes the signed URL column
func (t *TursoDB) ExpireCloudObject(ctx context.Context, id string) {
	if !t.IsConfigured() {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	query := `UPDATE cloud_objects SET status = 'expired', download_url = NULL, purged_at = CURRENT_TIMESTAMP WHERE id = ?;`
	_ = t.execute(bgCtx, query, id)
}
