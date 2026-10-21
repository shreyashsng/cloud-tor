package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cloud-tor/pkg/cache"
	"cloud-tor/pkg/db"
	"cloud-tor/pkg/engine"
	"cloud-tor/pkg/services"
	"cloud-tor/pkg/storage"
	"github.com/anacrolix/torrent/metainfo"
)

type Server struct {
	engine     *engine.Engine
	storage    *storage.StorageManager
	jobs       *JobManager
	cache      *cache.RedisCache
	db         *db.TursoDB
	pla        *services.PLAService
	vid        *services.VIDService
	publicURL  string
	defaultVid string
	defaultPla string
	adminCred  string
	uploadSem  chan struct{}
	clientFS   embed.FS
}

func NewServer(eng *engine.Engine, storageMgr *storage.StorageManager, redisCache *cache.RedisCache, tursoDB *db.TursoDB, publicURL string, clientFS embed.FS) *Server {
	return &Server{
		engine:     eng,
		storage:    storageMgr,
		jobs:       NewJobManager(),
		cache:      redisCache,
		db:         tursoDB,
		pla:        services.NewPLAService(),
		vid:        services.NewVIDService(),
		publicURL:  strings.TrimRight(publicURL, "/"),
		defaultVid: os.Getenv("VID_API_KEY"),
		defaultPla: os.Getenv("PLA_API_KEY"),
		adminCred:  os.Getenv("TOR_CRED"),
		uploadSem:  make(chan struct{}, 4), // Worker semaphore: max 4 concurrent uploads to B2
		clientFS:   clientFS,
	}
}

func (s *Server) checkAdminAuth(r *http.Request) bool {
	if s.adminCred == "" {
		return true // If no TOR_CRED is set, allow access
	}
	// 1. HTTP Basic Auth (username can be anything or "admin", password must match TOR_CRED)
	_, pass, ok := r.BasicAuth()
	if ok && pass == s.adminCred {
		return true
	}
	// 2. Authorization: Bearer <TOR_CRED>
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == s.adminCred {
		return true
	}
	// 3. X-Admin-Auth header
	if r.Header.Get("X-Admin-Auth") == s.adminCred {
		return true
	}
	// 4. URL Query Param ?key=<TOR_CRED>
	if r.URL.Query().Get("key") == s.adminCred {
		return true
	}
	return false
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.HandleHealth)
	mux.HandleFunc("POST /api/torrent/resolve", s.HandleResolve)
	mux.HandleFunc("GET /api/stream/", s.HandleStream)
	mux.HandleFunc("GET /api/user/quota", s.HandleUserQuota)

	// Public Storage Status (Sanitized: returns only isConfigured, no bucket/object telemetry)
	mux.HandleFunc("GET /api/storage/status", s.HandleStorageStatus)

	// Admin Endpoints (Strictly Protected by TOR_CRED)
	mux.HandleFunc("GET /api/admin/storage/status", s.HandleAdminStorageStatus)
	mux.HandleFunc("POST /api/storage/config", s.HandleStorageConfig)
	mux.HandleFunc("GET /cg/config-tor", s.HandleConfigTor)

	// Ingestion & Job Status
	mux.HandleFunc("GET /api/jobs/", s.HandleGetJob)
	mux.HandleFunc("POST /api/ingest/pla", s.HandleIngestPLA)
	mux.HandleFunc("POST /api/ingest/vid", s.HandleIngestVID)
}

func (s *Server) HandleConfigTor(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdminAuth(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted Cloud-Tor Administration Console"`)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("401 Unauthorized: Valid TOR_CRED credentials required to access the admin console."))
		return
	}

	content, err := s.clientFS.ReadFile("client/config.html")
	if err != nil {
		http.Error(w, "Admin console not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	hasStorage := false
	if s.storage != nil {
		hasStorage = s.storage.HasConfiguredBuckets()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"memory": map[string]string{
			"alloc":     engine.FormatBytes(int64(m.Alloc)),
			"sys":       engine.FormatBytes(int64(m.Sys)),
			"heapAlloc": engine.FormatBytes(int64(m.HeapAlloc)),
			"heapSys":   engine.FormatBytes(int64(m.HeapSys)),
		},
		"configured": map[string]any{
			"hasStorage": hasStorage,
			"publicUrl":  s.publicURL,
		},
	})
}

type resolveRequest struct {
	Magnet string `json:"magnet"`
}

func (s *Server) HandleResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	reqMagnet := strings.TrimSpace(req.Magnet)
	if reqMagnet == "" {
		http.Error(w, `{"error":"please provide a valid magnet link or info-hash"}`, http.StatusBadRequest)
		return
	}

	// 1. Check Redis Cache first for instant resolution
	var candidateHash string
	if len(reqMagnet) == 40 {
		candidateHash = strings.ToLower(reqMagnet)
	} else if m, err := metainfo.ParseMagnetURI(reqMagnet); err == nil {
		candidateHash = strings.ToLower(m.InfoHash.HexString())
	}

	if candidateHash != "" && s.cache != nil {
		var cachedMeta engine.TorrentMeta
		found, err := s.cache.GetJSON(r.Context(), "torrent:meta:"+candidateHash, &cachedMeta)
		if err == nil && found && len(cachedMeta.Files) > 0 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"cached":  true,
				"torrent": cachedMeta,
			})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()

	meta, err := s.engine.ResolveMagnet(ctx, reqMagnet)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Security validation: check if torrent exclusively contains blocked scripts
	allDisallowed := true
	for _, f := range meta.Files {
		if !f.IsDisallowed {
			allDisallowed = false
			break
		}
	}
	if allDisallowed && len(meta.Files) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "This torrent contains only restricted script files (.bat, .cmd, .sh, etc.) which cannot be processed for security reasons.",
		})
		return
	}

	// 2. Cache resolved metadata in Upstash Redis (24-hour TTL)
	if s.cache != nil && meta != nil {
		_ = s.cache.SetJSON(context.Background(), "torrent:meta:"+meta.InfoHash, meta, 86400)
	}

	// 3. Persistently record magnet and metadata in Turso DB for internal analytics/marketing
	if s.db != nil && meta != nil {
		clientIP := r.RemoteAddr
		userAgent := r.UserAgent()
		s.db.RecordTorrent(context.Background(), meta.InfoHash, reqMagnet, meta.Name, meta.TotalLength, meta.FormattedSize, len(meta.Files), clientIP, userAgent)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"torrent": meta,
	})
}

func (s *Server) HandleStream(w http.ResponseWriter, r *http.Request) {
	// Path pattern: /api/stream/{infoHash}/{fileIndex}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.Error(w, "invalid stream URL format: expected /api/stream/{infoHash}/{fileIndex}", http.StatusBadRequest)
		return
	}

	infoHash := parts[2]
	fileIndex, err := strconv.Atoi(parts[3])
	if err != nil {
		http.Error(w, "invalid file index", http.StatusBadRequest)
		return
	}

	file, err := s.engine.GetFile(infoHash, fileIndex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	filename := filepath.Base(file.Path())
	if isDisallowed, reason := engine.IsDisallowedScript(filename); isDisallowed {
		http.Error(w, fmt.Sprintf("Access denied: %s", reason), http.StatusForbidden)
		return
	}

	reader, _, err := s.engine.GetStreamReader(infoHash, fileIndex, r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", engine.GetMimeType(filename))

	if r.URL.Query().Get("download") == "1" || r.URL.Query().Get("dl") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
	}

	// Go's built-in http.ServeContent natively handles HTTP 206 Partial Content (Range requests)
	http.ServeContent(w, r, filename, time.Time{}, reader)
}

type storagePrepareRequest struct {
	InfoHash  string `json:"infoHash"`
	FileIndex int    `json:"fileIndex"`
}

func (s *Server) HandleStoragePrepare(w http.ResponseWriter, r *http.Request) {
	var req storagePrepareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	if s.storage == nil || !s.storage.HasConfiguredBuckets() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":      false,
			"error":        "Storage service not ready: please configure storage credentials at /cg/config-tor.",
			"unconfigured": true,
		})
		return
	}

	file, err := s.engine.GetFile(req.InfoHash, req.FileIndex)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}

	// ENFORCE 5 GB MAX FILE SIZE LIMIT
	if file.Length() > engine.MaxFileSizeBytes {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   fmt.Sprintf("File size (%s) exceeds maximum allowed limit of 5 GB.", engine.FormatBytes(file.Length())),
		})
		return
	}

	filename := filepath.Base(file.Path())
	if isDisallowed, reason := engine.IsDisallowedScript(filename); isDisallowed {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Access denied: %s", reason),
		})
		return
	}

	// Enforce 5 Free Downloads Limit per Client IP
	clientIP := getClientIP(r)
	if s.cache != nil && clientIP != "" {
		quotaKey := "quota:downloads:" + clientIP
		used, err := s.cache.GetInt(r.Context(), quotaKey)
		if err == nil && used >= 5 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":       false,
				"quotaExceeded": true,
				"used":          used,
				"limit":         5,
				"error":         "Free limit reached: You have used your 5 free cloud downloads.",
			})
			return
		}
	}

	// Instant cache hit: check if file is already active in storage and unexpired
	if activeObj := s.storage.GetLedger().FindActive(req.InfoHash, req.FileIndex); activeObj != nil {
		freshURL, err := s.storage.RefreshPresignedURL(r.Context(), activeObj)
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"cached":  true,
				"result": map[string]any{
					"downloadUrl":      freshURL,
					"fileName":         activeObj.FileName,
					"sizeBytes":        activeObj.SizeBytes,
					"formattedSize":    engine.FormatBytes(activeObj.SizeBytes),
					"expiresAt":        activeObj.ExpiresAt.Format(time.RFC3339),
					"expiresInSeconds": int(activeObj.RemainingDuration().Seconds()),
					"message":          "Your download link is ready! It will remain active for 6 hours before being automatically purged.",
				},
			})
			return
		}
	}

	job := s.jobs.CreateJob("storage")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"jobId":   job.ID,
		"message": "Direct cloud download preparation started",
	})

	go func() {
		// Acquire worker semaphore slot (max 4 concurrent uploads to B2)
		// If all 4 slots are busy, enqueue and mark job status as 'queued'
		select {
		case s.uploadSem <- struct{}{}:
			s.jobs.StartJob(job.ID)
		default:
			s.jobs.EnqueueJob(job.ID)
			s.uploadSem <- struct{}{}
			s.jobs.StartJob(job.ID)
		}
		defer func() { <-s.uploadSem }()

		// Increment user's free downloads count in Upstash Redis
		if s.cache != nil && clientIP != "" {
			_, _ = s.cache.Incr(context.Background(), "quota:downloads:"+clientIP)
		}

		reader, _, err := s.engine.GetStreamReader(req.InfoHash, req.FileIndex, context.Background())
		if err != nil {
			s.jobs.FailJob(job.ID, fmt.Errorf("failed to initiate torrent stream: %w", err))
			return
		}
		defer reader.Close()

		mimeType := engine.GetMimeType(filename)
		storedObj, err := s.storage.UploadStream(
			context.Background(),
			req.InfoHash,
			req.FileIndex,
			filename,
			file.Length(),
			mimeType,
			reader,
			func(uploaded, total int64, percent, speed float64) {
				s.jobs.UpdateProgress(job.ID, uploaded, total, percent, speed)
			},
		)
		if err != nil {
			fmt.Printf("[Storage] Upload failed for Job %s: %v\n", job.ID, err)
			s.jobs.FailJob(job.ID, err)
			return
		}

		// 1. Record uploaded object in Turso DB (libSQL)
		if s.db != nil {
			s.db.RecordCloudObject(
				context.Background(),
				storedObj.ID,
				storedObj.InfoHash,
				storedObj.FileIndex,
				storedObj.FileName,
				storedObj.SizeBytes,
				storedObj.Bucket,
				storedObj.Key,
				storedObj.DownloadURL,
				storedObj.ExpiresAt,
			)
		}

		// 2. Prune local torrent chunk cache to maintain zero disk usage
		s.engine.PruneTorrent(req.InfoHash)

		s.jobs.CompleteJob(job.ID, map[string]any{
			"downloadUrl":      storedObj.DownloadURL,
			"fileName":         storedObj.FileName,
			"sizeBytes":        storedObj.SizeBytes,
			"formattedSize":    engine.FormatBytes(storedObj.SizeBytes),
			"expiresAt":        storedObj.ExpiresAt.Format(time.RFC3339),
			"expiresInSeconds": int(storedObj.RemainingDuration().Seconds()),
			"message":          "Your download link is ready! It will remain active for 6 hours before being automatically purged.",
		})
	}()
}

func (s *Server) HandleStorageStatus(w http.ResponseWriter, r *http.Request) {
	isConfigured := false
	if s.storage != nil {
		isConfigured = s.storage.HasConfiguredBuckets()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":      true,
		"isConfigured": isConfigured,
	})
}

func (s *Server) HandleAdminStorageStatus(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdminAuth(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted Cloud-Tor Administration Console"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "Unauthorized: Valid TOR_CRED credentials required.",
		})
		return
	}

	if s.storage == nil {
		http.Error(w, `{"error":"storage uninitialized"}`, http.StatusInternalServerError)
		return
	}

	active := s.storage.GetLedger().ListActive()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":        true,
		"bucketCount":    s.storage.BucketCount(),
		"isConfigured":   s.storage.HasConfiguredBuckets(),
		"activeObjects":  len(active),
		"retentionHours": 6,
	})
}

type storageConfigRequest struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Region    string `json:"region"`
	Endpoint  string `json:"endpoint"`
	Buckets   string `json:"buckets"`
}

func (s *Server) HandleStorageConfig(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdminAuth(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted Cloud-Tor Administration Console"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   "Unauthorized: Valid TOR_CRED credentials required to update storage configuration.",
		})
		return
	}

	var req storageConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON request"}`, http.StatusBadRequest)
		return
	}

	if req.AccessKey == "" || req.SecretKey == "" || req.Buckets == "" {
		http.Error(w, `{"error":"AccessKey, SecretKey, and Buckets are required"}`, http.StatusBadRequest)
		return
	}

	if req.Region == "" {
		if strings.Contains(req.Endpoint, "backblazeb2.com") {
			req.Region = "eu-central-003"
		} else {
			req.Region = "us-east-1"
		}
	}
	if req.Endpoint == "" {
		if strings.Contains(req.Region, "-00") {
			req.Endpoint = fmt.Sprintf("https://s3.%s.backblazeb2.com", req.Region)
		} else if req.Region != "us-east-1" {
			req.Endpoint = fmt.Sprintf("https://s3.%s.wasabisys.com", req.Region)
		} else {
			req.Endpoint = "https://s3.wasabisys.com"
		}
	}

	bucketNames := strings.Split(req.Buckets, ",")
	count := 0
	for _, b := range bucketNames {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		err := s.storage.AddOrUpdateBucket(storage.BucketConfig{
			Name:         b,
			Region:       req.Region,
			Endpoint:     req.Endpoint,
			AccessKey:    req.AccessKey,
			SecretKey:    req.SecretKey,
			UsePathStyle: true,
		})
		if err == nil {
			count++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"configured":  count,
		"bucketCount": s.storage.BucketCount(),
		"message":     fmt.Sprintf("Successfully configured %d storage bucket(s)", count),
	})
}

func (s *Server) HandleIngestPLA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InfoHash  string `json:"infoHash"`
		FileIndex int    `json:"fileIndex"`
		APIKey    string `json:"apiKey"`
		Mode      string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	key := req.APIKey
	if key == "" {
		key = s.defaultPla
	}
	if key == "" {
		http.Error(w, `{"error":"PLA API Key is missing"}`, http.StatusBadRequest)
		return
	}

	file, err := s.engine.GetFile(req.InfoHash, req.FileIndex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	filename := filepath.Base(file.Path())
	if isDisallowed, reason := engine.IsDisallowedScript(filename); isDisallowed {
		http.Error(w, fmt.Sprintf("Access denied: %s", reason), http.StatusForbidden)
		return
	}

	job := s.jobs.CreateJob("pla")

	if req.Mode == "remote" {
		streamURL := fmt.Sprintf("%s/api/stream/%s/%d/%s?download=1", s.publicURL, req.InfoHash, req.FileIndex, url.PathEscape(filename))
		res, err := s.pla.RemoteUpload(key, streamURL)
		if err != nil {
			s.jobs.FailJob(job.ID, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "jobId": job.ID})
			return
		}

		s.jobs.CompleteJob(job.ID, res)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "jobId": job.ID, "result": res})
		return
	}

	reader, _, err := s.engine.GetStreamReader(req.InfoHash, req.FileIndex, context.Background())
	if err != nil {
		s.jobs.FailJob(job.ID, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"jobId":   job.ID,
		"message": "Streaming upload started",
	})

	go func() {
		defer reader.Close()
		res, err := s.pla.StreamUpload(key, filename, file.Length(), reader, func(uploaded, total int64, percent, speed float64) {
			s.jobs.UpdateProgress(job.ID, uploaded, total, percent, speed)
		})
		if err != nil {
			s.jobs.FailJob(job.ID, err)
			return
		}
		s.jobs.CompleteJob(job.ID, res)
	}()
}

func (s *Server) HandleIngestVID(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InfoHash  string `json:"infoHash"`
		FileIndex int    `json:"fileIndex"`
		APIKey    string `json:"apiKey"`
		Mode      string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	key := req.APIKey
	if key == "" {
		key = s.defaultVid
	}
	if key == "" {
		http.Error(w, `{"error":"VID API Key is missing"}`, http.StatusBadRequest)
		return
	}

	file, err := s.engine.GetFile(req.InfoHash, req.FileIndex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	filename := filepath.Base(file.Path())
	if isDisallowed, reason := engine.IsDisallowedScript(filename); isDisallowed {
		http.Error(w, fmt.Sprintf("Access denied: %s", reason), http.StatusForbidden)
		return
	}

	job := s.jobs.CreateJob("vid")

	if req.Mode == "remote" {
		streamURL := fmt.Sprintf("%s/api/stream/%s/%d/%s?download=1", s.publicURL, req.InfoHash, req.FileIndex, url.PathEscape(filename))
		res, err := s.vid.RemoteUpload(key, streamURL, filename)
		if err != nil {
			s.jobs.FailJob(job.ID, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "jobId": job.ID})
			return
		}

		s.jobs.CompleteJob(job.ID, res)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "jobId": job.ID, "result": res})
		return
	}

	reader, _, err := s.engine.GetStreamReader(req.InfoHash, req.FileIndex, context.Background())
	if err != nil {
		s.jobs.FailJob(job.ID, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"jobId":   job.ID,
		"message": "Streaming upload started",
	})

	go func() {
		defer reader.Close()
		res, err := s.vid.StreamUpload(key, filename, file.Length(), reader, func(uploaded, total int64, percent, speed float64) {
			s.jobs.UpdateProgress(job.ID, uploaded, total, percent, speed)
		})
		if err != nil {
			s.jobs.FailJob(job.ID, err)
			return
		}
		s.jobs.CompleteJob(job.ID, res)
	}()
}

func (s *Server) HandleGetJob(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "missing jobId in URL", http.StatusBadRequest)
		return
	}

	jobID := parts[2]
	job, exists := s.jobs.GetJob(jobID)
	if !exists {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "Job not found"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"job":     job,
	})
}

func (s *Server) HandleUserQuota(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)
	used := 0
	if s.cache != nil && clientIP != "" {
		used, _ = s.cache.GetInt(r.Context(), "quota:downloads:"+clientIP)
	}
	remaining := 5 - used
	if remaining < 0 {
		remaining = 0
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":   true,
		"used":      used,
		"limit":     5,
		"remaining": remaining,
	})
}

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return strings.TrimSpace(xrip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

