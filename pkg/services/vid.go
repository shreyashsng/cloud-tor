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
	vidAPIBase   = decodeEndpoint("aHR0cHM6Ly9hcGkudmlkYXJhLnNvL3Yx")
	vidWebBase   = decodeEndpoint("aHR0cHM6Ly92aWRhcmEudG8=")
	vidEmbedBase = decodeEndpoint("aHR0cHM6Ly92aWRhcmEudG8vZQ==")
)

type VIDService struct {
	client *http.Client
}

func NewVIDService() *VIDService {
	return &VIDService{
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

type VIDResult struct {
	Success  bool   `json:"success"`
	FileCode string `json:"filecode"`
	URL      string `json:"url"`
	EmbedURL string `json:"embedUrl"`
	Raw      any    `json:"raw,omitempty"`
}

type vidUploadServerResponse struct {
	Result struct {
		UploadServer string `json:"upload_server"`
	} `json:"result"`
	Msg string `json:"msg"`
}

// GetUploadServer retrieves the designated upload endpoint from VID API.
func (s *VIDService) GetUploadServer(apiKey string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("VID API key is missing")
	}

	endpoint := fmt.Sprintf("%s/upload/server?api_key=%s", vidAPIBase, url.QueryEscape(apiKey))
	resp, err := s.client.Get(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to contact VID API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read VID server response: %w", err)
	}

	var data vidUploadServerResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("failed to parse VID response: %w", err)
	}

	if data.Result.UploadServer != "" {
		return data.Result.UploadServer, nil
	}

	return "", fmt.Errorf("VID server error: %s", data.Msg)
}

// RemoteUpload instructs VID cloud servers to fetch the video stream directly.
func (s *VIDService) RemoteUpload(apiKey, streamURL, title string) (*VIDResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("VID API key is missing")
	}

	endpoint := fmt.Sprintf("%s/upload/url?api_key=%s&url=%s", vidAPIBase, url.QueryEscape(apiKey), url.QueryEscape(streamURL))
	if title != "" {
		endpoint += fmt.Sprintf("&title=%s", url.QueryEscape(title))
	}
	resp, err := s.client.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to contact VID remote upload: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var raw map[string]any
	_ = json.Unmarshal(body, &raw)

	filecode := ""
	link := ""
	if dataMap, ok := raw["data"].(map[string]any); ok {
		if fc, ok := dataMap["filecode"].(string); ok {
			filecode = fc
		}
		if lk, ok := dataMap["link"].(string); ok {
			link = lk
		}
	}
	if filecode == "" {
		if resMap, ok := raw["result"].(map[string]any); ok {
			if fc, ok := resMap["filecode"].(string); ok {
				filecode = fc
			}
		}
	}
	if filecode == "" {
		if fc, ok := raw["filecode"].(string); ok {
			filecode = fc
		}
	}

	watchURL := link
	if watchURL == "" && filecode != "" {
		watchURL = fmt.Sprintf("%s/%s", vidWebBase, filecode)
	}
	embedURL := ""
	if filecode != "" {
		embedURL = fmt.Sprintf("%s/%s", vidEmbedBase, filecode)
	}

	return &VIDResult{
		Success:  true,
		FileCode: filecode,
		URL:      watchURL,
		EmbedURL: embedURL,
		Raw:      raw,
	}, nil
}

// StreamUpload streams directly to VID upload server via multipart POST with zero disk caching.
func (s *VIDService) StreamUpload(
	apiKey, filename string,
	totalLength int64,
	fileReader io.Reader,
	onProgress func(uploaded, total int64, percent float64, speed float64),
) (*VIDResult, error) {
	uploadURL, err := s.GetUploadServer(apiKey)
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
	_ = calcWriter.WriteField("api_key", apiKey)
	_ = calcWriter.WriteField("key", apiKey)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(filename)))
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

		_ = writer.WriteField("api_key", apiKey)
		_ = writer.WriteField("key", apiKey)

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
		return nil, fmt.Errorf("VID upload POST failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read VID upload response: %w", err)
	}

	var raw map[string]any
	_ = json.Unmarshal(respBody, &raw)

	filecode := ""
	if fc, ok := raw["filecode"].(string); ok {
		filecode = fc
	} else if resMap, ok := raw["result"].(map[string]any); ok {
		if fc, ok := resMap["filecode"].(string); ok {
			filecode = fc
		}
	}

	cleanCode := filecode
	if strings.Contains(filecode, "/") {
		parts := strings.Split(filecode, "/")
		cleanCode = parts[len(parts)-1]
	}

	watchURL := fmt.Sprintf("%s/%s", vidWebBase, cleanCode)
	embedURL := fmt.Sprintf("%s/%s", vidEmbedBase, cleanCode)
	if strings.HasPrefix(filecode, "http") {
		embedURL = filecode
	}

	return &VIDResult{
		Success:  true,
		FileCode: cleanCode,
		URL:      watchURL,
		EmbedURL: embedURL,
		Raw:      raw,
	}, nil
}
