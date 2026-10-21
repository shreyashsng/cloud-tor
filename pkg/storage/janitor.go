package storage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Janitor periodically checks the ledger and deletes objects that reached their 6-hour expiration
type Janitor struct {
	manager  *StorageManager
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
	onExpire func(id string)
}

// NewJanitor creates a background janitor instance
func NewJanitor(manager *StorageManager, interval time.Duration, onExpire func(id string)) *Janitor {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return &Janitor{
		manager:  manager,
		interval: interval,
		stopCh:   make(chan struct{}),
		onExpire: onExpire,
	}
}

// Start launches the cleanup worker in the background
func (j *Janitor) Start() {
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		fmt.Printf("[Storage Janitor] Worker active. Automatic 6-hour purge sweep running every %v.\n", j.interval)

		// Immediate purge on startup for any leftover expired objects
		j.sweep()

		ticker := time.NewTicker(j.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				j.sweep()
			case <-j.stopCh:
				fmt.Println("[Storage Janitor] Stopped.")
				return
			}
		}
	}()
}

// Stop halts the janitor gracefully
func (j *Janitor) Stop() {
	close(j.stopCh)
	j.wg.Wait()
}

func (j *Janitor) sweep() {
	expired := j.manager.GetLedger().GetExpired()
	if len(expired) == 0 {
		return
	}

	fmt.Printf("[Storage Janitor] Found %d expired objects exceeding 6-hour retention. Purging from B2 cloud storage...\n", len(expired))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, obj := range expired {
		err := j.manager.DeleteObject(ctx, obj.Bucket, obj.Key)
		if err != nil {
			fmt.Printf("[Storage Janitor] Warning: Failed to delete %s/%s from B2: %v\n", obj.Bucket, obj.Key, err)
		} else {
			fmt.Printf("[Storage Janitor] Purged expired object: %s/%s (Filename: %s, Expired At: %s)\n",
				obj.Bucket, obj.Key, obj.FileName, obj.ExpiresAt.Format(time.RFC3339))
		}
		// Notify database hook to flush signed URL
		if j.onExpire != nil {
			j.onExpire(obj.ID)
		}
		// Remove from persistent ledger
		_ = j.manager.GetLedger().Remove(obj.ID)
	}
}
