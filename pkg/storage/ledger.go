package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StoredObject represents an object stored in Wasabi/S3 with a strict retention lifetime
type StoredObject struct {
	ID          string    `json:"id"`
	Bucket      string    `json:"bucket"`
	Key         string    `json:"key"`
	FileName    string    `json:"fileName"`
	SizeBytes   int64     `json:"sizeBytes"`
	InfoHash    string    `json:"infoHash"`
	FileIndex   int       `json:"fileIndex"`
	DownloadURL string    `json:"downloadUrl"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// IsExpired checks if the object has exceeded its retention time
func (o *StoredObject) IsExpired() bool {
	return time.Now().After(o.ExpiresAt)
}

// RemainingDuration returns remaining time before auto-deletion
func (o *StoredObject) RemainingDuration() time.Duration {
	rem := time.Until(o.ExpiresAt)
	if rem < 0 {
		return 0
	}
	return rem
}

// Ledger manages persistent tracking of stored objects and their expiration
type Ledger struct {
	filePath string
	mu       sync.RWMutex
	objects  map[string]*StoredObject
}

// NewLedger initializes or loads the storage ledger
func NewLedger(dataDir string) (*Ledger, error) {
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "cloud-tor-cache")
	}
	_ = os.MkdirAll(dataDir, 0755)

	filePath := filepath.Join(dataDir, "storage_ledger.json")
	l := &Ledger{
		filePath: filePath,
		objects:  make(map[string]*StoredObject),
	}

	if err := l.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load storage ledger: %w", err)
	}

	return l, nil
}

func (l *Ledger) load() error {
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}

	var list []*StoredObject
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}

	for _, obj := range list {
		l.objects[obj.ID] = obj
	}
	return nil
}

func (l *Ledger) save() error {
	list := make([]*StoredObject, 0, len(l.objects))
	for _, obj := range l.objects {
		list = append(list, obj)
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := l.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, l.filePath)
}

// Record saves a new stored object record
func (l *Ledger) Record(obj *StoredObject) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.objects[obj.ID] = obj
	return l.save()
}

// FindActive looks for an unexpired object matching the info-hash and file index
func (l *Ledger) FindActive(infoHash string, fileIndex int) *StoredObject {
	l.mu.RLock()
	defer l.mu.RUnlock()

	for _, obj := range l.objects {
		if obj.InfoHash == infoHash && obj.FileIndex == fileIndex && !obj.IsExpired() {
			return obj
		}
	}
	return nil
}

// GetExpired returns all objects that have exceeded their retention duration
func (l *Ledger) GetExpired() []*StoredObject {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var expired []*StoredObject
	now := time.Now()
	for _, obj := range l.objects {
		if now.After(obj.ExpiresAt) {
			expired = append(expired, obj)
		}
	}
	return expired
}

// Remove deletes an object record from the ledger
func (l *Ledger) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.objects, id)
	return l.save()
}

// ListActive returns all unexpired objects
func (l *Ledger) ListActive() []*StoredObject {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var active []*StoredObject
	for _, obj := range l.objects {
		if !obj.IsExpired() {
			active = append(active, obj)
		}
	}
	return active
}
