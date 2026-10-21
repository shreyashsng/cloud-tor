package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// Obfuscated endpoints to ensure provider anonymity
var (
	plaAPIBase   = decodeEndpoint("aHR0cHM6Ly9hcGkucGxheW1hdGUudG8=")
	plaWebBase   = decodeEndpoint("aHR0cHM6Ly9wbGF5bWF0ZS50bw==")
	plaEmbedBase = decodeEndpoint("aHR0cHM6Ly9wbGF5bWF0ZS50by9l")
)

type PLAService struct {
	client *http.Client
}

func NewPLAService() *PLAService {
	return &PLAService{
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 25,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
				ForceAttemptHTTP2:   true,
			},
			Timeout: 0,
		},
	}
}

type PLAResult struct {
	Success  bool   `json:"success"`
	FileCode string `json:"filecode"`
	URL      string `json:"url"`
	EmbedURL string `json:"embedUrl"`
	Raw      any    `json:"raw,omitempty"`
}

type plaUploadServerResponse struct {
	Status int    `json:"status"`
	Msg    string `json:"msg"`
	Result struct {
		UploadURL string `json:"upload_url"`
		KeyField  string `json:"key_field"`
		FileField string `json:"file_field"`
	} `json:"result"`
	UploadURL string `json:"upload_url"`
	KeyField  string `json:"key_field"`
	FileField string `json:"file_field"`
}

// GetUploadServer retrieves the designated upload endpoint from PLA API.
func (s *PLAService) GetUploadServer(apiKey string) (uploadURL, keyField, fileField string, err error) {
	if apiKey == "" {
		return "", "", "", fmt.Errorf("PLA API key is missing")
	}

	reqURL := fmt.Sprintf("%s/upload/server?key=%s", plaAPIBase, url.QueryEscape(apiKey))
	resp, err := s.client.Get(reqURL)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to reach PLA API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read PLA server response: %w", err)
	}

	var data plaUploadServerResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return "", "", "", fmt.Errorf("failed to parse PLA response: %w", err)
	}

	if data.Status == 401 || data.Msg == "Invalid API key" {
		return "", "", "", fmt.Errorf("invalid PLA API key")
	}

	u := data.Result.UploadURL
	if u == "" {
		u = data.UploadURL
	}
	kf := data.Result.KeyField
	if kf == "" {
		kf = data.KeyField
	}
	if kf == "" {
		kf = "api_key"
	}
	ff := data.Result.FileField
	if ff == "" {
		ff = data.FileField
	}
	if ff == "" {
		ff = "file"
	}

	if u == "" {
		return "", "", "", fmt.Errorf("PLA upload server not found in response: %s", string(body))
	}

	return u, kf, ff, nil
}

// RemoteUpload instructs PLA cloud servers to fetch the stream URL directly using HTTP Range requests.
func (s *PLAService) RemoteUpload(apiKey, streamURL string) (*PLAResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("PLA API key is missing")
	}

	endpoint := fmt.Sprintf("%s/upload/url?key=%s&url=%s", plaAPIBase, url.QueryEscape(apiKey), url.QueryEscape(streamURL))
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to contact PLA remote upload: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var raw map[string]any
	_ = json.Unmarshal(body, &raw)

	msg, _ := raw["msg"].(string)
	if status, ok := raw["status"].(float64); ok && status != 200 && msg != "" {
		return nil, fmt.Errorf("PLA error: %s", msg)
	}

	filecode := ""
	if resMap, ok := raw["result"].(map[string]any); ok {
		if fc, ok := resMap["filecode"].(string); ok {
			filecode = fc
		} else if fc, ok := resMap["file_code"].(string); ok {
			filecode = fc
		}
	}

	if filecode == "" {
		if fc, ok := raw["filecode"].(string); ok {
			filecode = fc
		}
	}

	cleanCode := filecode
	if strings.Contains(filecode, "/") {
		parts := strings.Split(filecode, "/")
		cleanCode = parts[len(parts)-1]
	}

	playURL := fmt.Sprintf("%s/%s", plaWebBase, cleanCode)
	embedURL := fmt.Sprintf("%s/%s", plaEmbedBase, cleanCode)
	if strings.HasPrefix(filecode, "http") {
		embedURL = filecode
	}

	return &PLAResult{
		Success:  true,
		FileCode: cleanCode,
		URL:      playURL,
		EmbedURL: embedURL,
		Raw:      raw,
	}, nil
}

// StreamUpload streams directly to PLA upload server via multipart POST.
func (s *PLAService) StreamUpload(
	apiKey, filename string,
	totalLength int64,
	fileReader io.Reader,
	onProgress func(uploaded, total int64, percent float64, speed float64),
) (*PLAResult, error) {
	uploadURL, keyField, fileField, err := s.GetUploadServer(apiKey)
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	boundary := writer.Boundary()

	// Calculate exact Content-Length so the upload server accepts stream directly without chunking
	var headerBuf bytes.Buffer
	calcWriter := multipart.NewWriter(&headerBuf)
	calcWriter.SetBoundary(boundary)
	_ = calcWriter.WriteField(keyField, apiKey)
	if keyField != "key" {
		_ = calcWriter.WriteField("key", apiKey)
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(fileField), escapeQuotes(filename)))
	h.Set("Content-Type", "video/x-matroska")
	_, _ = calcWriter.CreatePart(h)
	headerLen := int64(headerBuf.Len())
	trailerLen := int64(len(fmt.Sprintf("\r\n--%s--\r\n", boundary)))
	totalContentLength := headerLen + totalLength + trailerLen

	progressR := &ProgressReader{
		Reader:     fileReader,
		Total:      totalLength,
		OnProgress: onProgress,
		lastTime:   time.Now(),
	}

	go func() {
		defer pw.Close()

		if err := writer.WriteField(keyField, apiKey); err != nil {
			pw.CloseWithError(err)
			return
		}
		if keyField != "key" {
			_ = writer.WriteField("key", apiKey)
		}

		part, err := writer.CreatePart(h)
		if err != nil {
			pw.CloseWithError(err)
			return
		}

		// Use buffer pool for high throughput zero-allocation streaming
		bufPtr := bufPool.Get().(*[]byte)
		defer bufPool.Put(bufPtr)

		if _, err := io.CopyBuffer(part, progressR, *bufPtr); err != nil {
			pw.CloseWithError(err)
			return
		}

		if err := writer.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
	}()

	req, err := http.NewRequest(http.MethodPost, uploadURL, pr)
	if err != nil {
		return nil, err
	}
	req.ContentLength = totalContentLength
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload POST failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read upload response: %w", err)
	}

	var raw map[string]any
	_ = json.Unmarshal(respBody, &raw)

	filecode := ""
	if fc, ok := raw["filecode"].(string); ok {
		filecode = fc
	} else if fc, ok := raw["file_code"].(string); ok {
		filecode = fc
	}

	cleanCode := filecode
	if strings.Contains(filecode, "/") {
		parts := strings.Split(filecode, "/")
		cleanCode = parts[len(parts)-1]
	}

	playURL := fmt.Sprintf("%s/%s", plaWebBase, cleanCode)
	embedURL := fmt.Sprintf("%s/%s", plaEmbedBase, cleanCode)
	if strings.HasPrefix(filecode, "http") {
		embedURL = filecode
	}

	return &PLAResult{
		Success:  true,
		FileCode: cleanCode,
		URL:      playURL,
		EmbedURL: embedURL,
		Raw:      raw,
	}, nil
}
