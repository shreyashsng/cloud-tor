package services

import (
	"encoding/base64"
	"io"
	"strings"
	"sync"
	"time"
)

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024) // 64KB high-speed I/O streaming buffer
		return &b
	},
}

func decodeEndpoint(encoded string) string {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return string(data)
}

func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s)
}

// ProgressReader wraps an io.Reader to report streaming bytes transferred with real-time throughput.
type ProgressReader struct {
	Reader     io.Reader
	Total      int64
	Uploaded   int64
	OnProgress func(uploaded, total int64, percent float64, speed float64)
	lastTime   time.Time
	lastBytes  int64
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 {
		pr.Uploaded += int64(n)
		now := time.Now()
		elapsed := now.Sub(pr.lastTime).Seconds()
		if elapsed >= 0.4 && pr.OnProgress != nil {
			delta := pr.Uploaded - pr.lastBytes
			speed := float64(delta) / elapsed
			percent := float64(0)
			if pr.Total > 0 {
				percent = (float64(pr.Uploaded) / float64(pr.Total)) * 100
			}
			pr.OnProgress(pr.Uploaded, pr.Total, percent, speed)
			pr.lastTime = now
			pr.lastBytes = pr.Uploaded
		}
	}
	return n, err
}
