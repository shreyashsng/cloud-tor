package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BucketConfig represents configuration for an S3-compatible bucket (e.g. Wasabi)
type BucketConfig struct {
	Name         string `json:"name"`
	Region       string `json:"region"`
	Endpoint     string `json:"endpoint"`
	AccessKey    string `json:"accessKey"`
	SecretKey    string `json:"secretKey"`
	UsePathStyle bool   `json:"usePathStyle"`
}

// StorageConfig holds global storage settings and bucket definitions
type StorageConfig struct {
	Buckets        []BucketConfig `json:"buckets"`
	RetentionHours time.Duration  `json:"retentionHours"`
	JanitorPeriod  time.Duration  `json:"janitorPeriod"`
}

// LoadConfig loads storage settings from environment variables
func LoadConfig() *StorageConfig {
	retentionHours := 6
	if v := os.Getenv("STORAGE_RETENTION_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			retentionHours = n
		}
	}

	cfg := &StorageConfig{
		RetentionHours: time.Duration(retentionHours) * time.Hour,
		JanitorPeriod:  2 * time.Minute, // Auto-delete sweep interval
		Buckets:        []BucketConfig{},
	}

	// 1. Check for JSON-based multi-bucket configuration
	if rawJSON := os.Getenv("STORAGE_CONFIG_JSON"); rawJSON != "" {
		var buckets []BucketConfig
		if err := json.Unmarshal([]byte(rawJSON), &buckets); err == nil && len(buckets) > 0 {
			for i := range buckets {
				if buckets[i].Region == "" {
					buckets[i].Region = "us-east-1"
				}
				if buckets[i].Endpoint == "" {
					buckets[i].Endpoint = "https://s3.wasabisys.com"
				}
				buckets[i].UsePathStyle = true
			}
			cfg.Buckets = append(cfg.Buckets, buckets...)
		}
	}

	// 2. Backblaze B2, generic S3, or Wasabi environment variables
	accessKey := firstNonEmpty(
		os.Getenv("B2_APPLICATION_KEY_ID"),
		os.Getenv("B2_KEY_ID"),
		os.Getenv("S3_ACCESS_KEY"),
		os.Getenv("WASABI_ACCESS_KEY"),
		os.Getenv("AWS_ACCESS_KEY_ID"),
	)
	secretKey := firstNonEmpty(
		os.Getenv("B2_APPLICATION_KEY"),
		os.Getenv("S3_SECRET_KEY"),
		os.Getenv("WASABI_SECRET_KEY"),
		os.Getenv("AWS_SECRET_ACCESS_KEY"),
	)
	region := firstNonEmpty(
		os.Getenv("B2_REGION"),
		os.Getenv("S3_REGION"),
		os.Getenv("WASABI_REGION"),
	)
	endpoint := firstNonEmpty(
		os.Getenv("B2_ENDPOINT"),
		os.Getenv("S3_ENDPOINT"),
		os.Getenv("WASABI_ENDPOINT"),
	)
	bucketStr := firstNonEmpty(
		os.Getenv("B2_BUCKETS"),
		os.Getenv("B2_BUCKET"),
		os.Getenv("S3_BUCKETS"),
		os.Getenv("S3_BUCKET"),
		os.Getenv("WASABI_BUCKETS"),
		os.Getenv("WASABI_BUCKET"),
	)

	if region == "" {
		if strings.Contains(endpoint, "backblazeb2.com") {
			region = "eu-central-003"
		} else {
			region = "us-east-1"
		}
	}
	if endpoint == "" {
		if strings.Contains(region, "-00") {
			endpoint = fmt.Sprintf("https://s3.%s.backblazeb2.com", region)
		} else if region != "us-east-1" {
			endpoint = fmt.Sprintf("https://s3.%s.wasabisys.com", region)
		} else {
			endpoint = "https://s3.wasabisys.com"
		}
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") && endpoint != "" {
		endpoint = "https://" + endpoint
	}

	if accessKey != "" && secretKey != "" && bucketStr != "" {
		bucketNames := strings.Split(bucketStr, ",")
		for _, bName := range bucketNames {
			bName = strings.TrimSpace(bName)
			if bName == "" {
				continue
			}
			// Avoid duplicate definitions
			exists := false
			for _, b := range cfg.Buckets {
				if b.Name == bName {
					exists = true
					break
				}
			}
			if !exists {
				cfg.Buckets = append(cfg.Buckets, BucketConfig{
					Name:         bName,
					Region:       region,
					Endpoint:     endpoint,
					AccessKey:    accessKey,
					SecretKey:    secretKey,
					UsePathStyle: true,
				})
			}
		}
	}

	return cfg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

