package main

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud-tor/pkg/api"
	"cloud-tor/pkg/cache"
	"cloud-tor/pkg/db"
	"cloud-tor/pkg/engine"
	"cloud-tor/pkg/storage"
)

//go:embed client/*
var clientFS embed.FS

func main() {
	loadEnvFile(".env")

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	publicURL := os.Getenv("PUBLIC_URL")
	if publicURL == "" {
		publicURL = fmt.Sprintf("http://localhost:%s", port)
	}
	publicURL = strings.TrimSpace(publicURL)
	if !strings.HasPrefix(publicURL, "http://") && !strings.HasPrefix(publicURL, "https://") {
		publicURL = "https://" + publicURL
	}

	log.Println("=======================================================")
	log.Printf("🚀 Cloud-Tor (Go Engine) starting on port %s", port)
	log.Printf("📡 Public URL: %s", publicURL)

	dataDir := filepathFromEnv("DATA_DIR", "/app/cache")

	// 1. Initialize Upstash Redis Cache Layer
	redisURL := os.Getenv("UPSTASH_REDIS_REST_URL")
	redisToken := os.Getenv("UPSTASH_REDIS_REST_TOKEN")
	redisCache := cache.NewRedisCache(redisURL, redisToken)
	if redisCache.IsConfigured() {
		log.Println("⚡ Upstash Redis Cache: Connected and active (24h metadata TTL) ✅")
	} else {
		log.Println("⚡ Upstash Redis Cache: Not configured (falling back to direct DHT resolution)")
	}

	// 2. Initialize Turso Marketing & Object DB (libSQL Pipeline v2)
	tursoURL := os.Getenv("TURSO_DATABASE_URL")
	tursoToken := os.Getenv("TURSO_AUTH_TOKEN")
	tursoDB := db.NewTursoDB(tursoURL, tursoToken)
	if tursoDB.IsConfigured() {
		ctxInit, cancelInit := context.WithTimeout(context.Background(), 10*time.Second)
		if err := tursoDB.InitSchema(ctxInit); err != nil {
			log.Printf("⚠️ Turso schema warning: %v", err)
		} else {
			log.Println("📊 Turso Database (libSQL): Connected and schema ready ✅")
		}
		cancelInit()
	} else {
		log.Println("📊 Turso Database: Not configured")
	}

	// 3. Initialize Backblaze B2 / S3 Cloud Storage Manager & 6-Hour Retention Janitor
	storageCfg := storage.LoadConfig()
	storageLedger, err := storage.NewLedger(dataDir)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize storage ledger: %v", err)
	}

	storageMgr, err := storage.NewStorageManager(storageCfg, storageLedger)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize storage manager: %v", err)
	}

	if storageMgr.HasConfiguredBuckets() {
		log.Printf("☁️ Cloud Storage: Configured with %d bucket(s) (Backblaze B2 / S3) ✅", storageMgr.BucketCount())
		log.Printf("⏱️ Retention Policy: %v (Auto-delete after 6 hours)", storageCfg.RetentionHours)
	} else {
		log.Println("☁️ Cloud Storage: No buckets configured yet (configure at /cg/config-tor)")
	}

	// 4. Background Janitor with Turso URL Flushing on 6-hour expiration
	janitor := storage.NewJanitor(storageMgr, storageCfg.JanitorPeriod, func(id string) {
		if tursoDB != nil && tursoDB.IsConfigured() {
			ctxExp, cancelExp := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancelExp()
			tursoDB.ExpireCloudObject(ctxExp, id)
			log.Printf("[Turso] Expired cloud object %s (signed URL cleared, metadata preserved) ✅", id)
		}
	})
	janitor.Start()
	defer janitor.Stop()

	log.Println("=======================================================")

	// 5. Initialize Torrent Engine (with 5 GB Max File Cap & Pruning)
	torrentEng, err := engine.NewEngine(dataDir, publicURL)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize torrent engine: %v", err)
	}
	defer torrentEng.Close()

	// 6. API Server & HTTP Mux
	apiServer := api.NewServer(torrentEng, storageMgr, redisCache, tursoDB, publicURL, clientFS)
	mux := http.NewServeMux()

	// Register API and Hidden Admin Endpoints
	apiServer.RegisterRoutes(mux)

	// Serve embedded client frontend
	subFS, err := fs.Sub(clientFS, "client")
	if err != nil {
		log.Fatalf("Fatal: failed to open embedded client filesystem: %v", err)
	}
	fileServer := http.FileServer(http.FS(subFS))

	// Catch-all for frontend with security headers & redirection
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		// Prevent direct access to /config.html or /config without the hidden admin prefix
		if r.URL.Path == "/config.html" || r.URL.Path == "/config" {
			http.Redirect(w, r, "/cg/config-tor", http.StatusTemporaryRedirect)
			return
		}

		// Never cache frontend assets in development/continuous deployment
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		fileServer.ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           withLoggingAndCORS(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Handle graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failure: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down Cloud-Tor server...")
}

func filepathFromEnv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func withLoggingAndCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("➡️ [%s] %s %s", r.RemoteAddr, r.Method, r.URL.RequestURI())
		}

		next.ServeHTTP(w, r)
	})
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			if (strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`)) || (strings.HasPrefix(v, `'`) && strings.HasSuffix(v, `'`)) {
				if len(v) >= 2 {
					v = v[1 : len(v)-1]
				}
			}
			// Only set if not already set in OS environment
			if os.Getenv(k) == "" && v != "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}

