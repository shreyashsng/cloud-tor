package storage

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

var sanitizeRegex = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// BucketClient wraps S3 client and uploader for a specific bucket
type BucketClient struct {
	Config        BucketConfig
	S3Client      *s3.Client
	PresignClient *s3.PresignClient
	Uploader      *manager.Uploader
}

// StorageManager coordinates uploads, presigned URLs, and deletions across one or more buckets
type StorageManager struct {
	cfg       *StorageConfig
	ledger    *Ledger
	clients   []*BucketClient
	nextIndex uint32
	mu        sync.RWMutex
}

// NewStorageManager constructs a new StorageManager with S3 clients for each configured bucket
func NewStorageManager(cfg *StorageConfig, ledger *Ledger) (*StorageManager, error) {
	sm := &StorageManager{
		cfg:    cfg,
		ledger: ledger,
	}

	for _, bCfg := range cfg.Buckets {
		client, err := createBucketClient(bCfg)
		if err != nil {
			fmt.Printf("[Storage] Warning: Failed to initialize bucket client for %s: %v\n", bCfg.Name, err)
			continue
		}
		sm.clients = append(sm.clients, client)
	}

	return sm, nil
}

func createBucketClient(bCfg BucketConfig) (*BucketClient, error) {
	endpoint := bCfg.Endpoint
	if endpoint == "" {
		endpoint = "https://s3.wasabisys.com"
	}
	region := bCfg.Region
	if region == "" {
		region = "us-east-1"
	}

	s3Client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(bCfg.AccessKey, bCfg.SecretKey, ""),
		UsePathStyle: bCfg.UsePathStyle,
	})

	presignClient := s3.NewPresignClient(s3Client)

	uploader := manager.NewUploader(s3Client, func(u *manager.Uploader) {
		u.PartSize = 10 * 1024 * 1024 // 10MB chunk parts
		u.Concurrency = 3
	})

	return &BucketClient{
		Config:        bCfg,
		S3Client:      s3Client,
		PresignClient: presignClient,
		Uploader:      uploader,
	}, nil
}

// AddOrUpdateBucket dynamically updates or registers a bucket client
func (sm *StorageManager) AddOrUpdateBucket(bCfg BucketConfig) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	client, err := createBucketClient(bCfg)
	if err != nil {
		return err
	}

	// Update existing bucket or add new
	for i, c := range sm.clients {
		if c.Config.Name == bCfg.Name {
			sm.clients[i] = client
			return nil
		}
	}

	sm.clients = append(sm.clients, client)
	return nil
}

// HasConfiguredBuckets returns true if at least one S3 bucket is configured
func (sm *StorageManager) HasConfiguredBuckets() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.clients) > 0
}

// BucketCount returns the number of active storage buckets
func (sm *StorageManager) BucketCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.clients)
}

// GetNextBucket gets the next available bucket in round-robin fashion
func (sm *StorageManager) GetNextBucket() (*BucketClient, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(sm.clients) == 0 {
		return nil, fmt.Errorf("no storage buckets configured: please configure your Wasabi access credentials")
	}

	idx := sm.nextIndex % uint32(len(sm.clients))
	sm.nextIndex++
	return sm.clients[idx], nil
}

func (sm *StorageManager) getBucketClient(bucketName string) *BucketClient {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, c := range sm.clients {
		if c.Config.Name == bucketName {
			return c
		}
	}
	return nil
}

// ProgressReader tracks streaming upload bytes and computes speed
type ProgressReader struct {
	reader     io.Reader
	totalBytes int64
	readBytes  int64
	startTime  time.Time
	lastReport time.Time
	progressFn func(uploaded, total int64, percent, speed float64)
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.readBytes += int64(n)
		now := time.Now()
		if now.Sub(pr.lastReport) >= 400*time.Millisecond || pr.readBytes == pr.totalBytes || err == io.EOF {
			pr.lastReport = now
			elapsed := now.Sub(pr.startTime).Seconds()
			speed := 0.0
			if elapsed > 0 {
				speed = float64(pr.readBytes) / elapsed
			}
			percent := 0.0
			if pr.totalBytes > 0 {
				percent = (float64(pr.readBytes) / float64(pr.totalBytes)) * 100.0
				if percent > 100.0 {
					percent = 100.0
				}
			}
			if pr.progressFn != nil {
				pr.progressFn(pr.readBytes, pr.totalBytes, percent, speed)
			}
		}
	}
	return n, err
}

// UploadStream uploads a file stream from torrent engine directly to Wasabi/S3
func (sm *StorageManager) UploadStream(
	ctx context.Context,
	infoHash string,
	fileIndex int,
	filename string,
	fileSize int64,
	mimeType string,
	reader io.Reader,
	progressFn func(uploaded, total int64, percent, speed float64),
) (*StoredObject, error) {
	bucketClient, err := sm.GetNextBucket()
	if err != nil {
		return nil, err
	}

	sanitizedName := sanitizeRegex.ReplaceAllString(filepath.Base(filename), "_")
	if sanitizedName == "" {
		sanitizedName = "file.bin"
	}

	// Key format: torrents/{infoHash}/{timestamp}_{filename}
	timestamp := time.Now().Unix()
	s3Key := fmt.Sprintf("torrents/%s/%d_%s", infoHash, timestamp, sanitizedName)

	wrappedReader := &ProgressReader{
		reader:     reader,
		totalBytes: fileSize,
		startTime:  time.Now(),
		lastReport: time.Now(),
		progressFn: progressFn,
	}

	fmt.Printf("[Storage] Uploading to Wasabi bucket '%s', key '%s' (%d bytes)...\n",
		bucketClient.Config.Name, s3Key, fileSize)

	disposition := fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(filename))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	_, err = bucketClient.Uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(bucketClient.Config.Name),
		Key:                aws.String(s3Key),
		Body:               wrappedReader,
		ContentType:        aws.String(mimeType),
		ContentDisposition: aws.String(disposition),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload object to Wasabi bucket %s: %w", bucketClient.Config.Name, err)
	}

	// Generate 6-hour Presigned GET URL
	retention := sm.cfg.RetentionHours
	if retention <= 0 {
		retention = 6 * time.Hour
	}

	presignedReq, err := bucketClient.PresignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(bucketClient.Config.Name),
		Key:                        aws.String(s3Key),
		ResponseContentDisposition: aws.String(disposition),
		ResponseContentType:        aws.String(mimeType),
	}, s3.WithPresignExpires(retention))
	if err != nil {
		return nil, fmt.Errorf("failed to generate presigned download URL: %w", err)
	}

	now := time.Now()
	storedObj := &StoredObject{
		ID:          uuid.New().String(),
		Bucket:      bucketClient.Config.Name,
		Key:         s3Key,
		FileName:    filepath.Base(filename),
		SizeBytes:   fileSize,
		InfoHash:    infoHash,
		FileIndex:   fileIndex,
		DownloadURL: presignedReq.URL,
		CreatedAt:   now,
		ExpiresAt:   now.Add(retention),
	}

	if err := sm.ledger.Record(storedObj); err != nil {
		fmt.Printf("[Storage] Warning: Failed to record object in ledger: %v\n", err)
	}

	fmt.Printf("[Storage] Upload complete: Object stored in '%s/%s'. Auto-delete scheduled at %s (6 hours).\n",
		storedObj.Bucket, storedObj.Key, storedObj.ExpiresAt.Format(time.RFC3339))

	return storedObj, nil
}

// RefreshPresignedURL generates a fresh signed URL for an existing active object with remaining lifetime
func (sm *StorageManager) RefreshPresignedURL(ctx context.Context, obj *StoredObject) (string, error) {
	client := sm.getBucketClient(obj.Bucket)
	if client == nil {
		return "", fmt.Errorf("bucket client for %s not found", obj.Bucket)
	}

	remaining := obj.RemainingDuration()
	if remaining <= 0 {
		return "", fmt.Errorf("object retention period has expired")
	}

	disposition := fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(obj.FileName))

	presignedReq, err := client.PresignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(obj.Bucket),
		Key:                        aws.String(obj.Key),
		ResponseContentDisposition: aws.String(disposition),
	}, s3.WithPresignExpires(remaining))
	if err != nil {
		return "", err
	}

	return presignedReq.URL, nil
}

// DeleteObject deletes an object from Wasabi/S3
func (sm *StorageManager) DeleteObject(ctx context.Context, bucketName, key string) error {
	client := sm.getBucketClient(bucketName)
	if client == nil {
		// Try any available client if specific client isn't matched
		sm.mu.RLock()
		if len(sm.clients) > 0 {
			client = sm.clients[0]
		}
		sm.mu.RUnlock()
	}
	if client == nil {
		return fmt.Errorf("no S3 client available to delete %s/%s", bucketName, key)
	}

	_, err := client.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	})
	return err
}

// GetLedger returns the underlying ledger
func (sm *StorageManager) GetLedger() *Ledger {
	return sm.ledger
}
