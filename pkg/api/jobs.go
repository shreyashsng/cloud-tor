package api

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type Job struct {
	ID               string  `json:"id"`
	Service          string  `json:"service"`
	Status           string  `json:"status"` // "queued", "starting", "uploading", "completed", "failed"
	QueuePosition    int     `json:"queuePosition,omitempty"`
	Progress         float64 `json:"progress"`
	UploadedBytes    int64   `json:"uploadedBytes"`
	TotalBytes       int64   `json:"totalBytes"`
	SpeedBytesPerSec float64 `json:"speedBytesPerSec"`
	Result           any     `json:"result,omitempty"`
	Error            string  `json:"error,omitempty"`
	StartTime        int64   `json:"startTime"`
}

type JobManager struct {
	mu    sync.RWMutex
	jobs  map[string]*Job
	queue []string
}

func NewJobManager() *JobManager {
	return &JobManager{
		jobs:  make(map[string]*Job),
		queue: make([]string, 0),
	}
}

func (jm *JobManager) CreateJob(service string) *Job {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	id := fmt.Sprintf("%s_%d_%s", service, time.Now().UnixMilli(), randomSuffix(5))
	job := &Job{
		ID:        id,
		Service:   service,
		Status:    "starting",
		StartTime: time.Now().UnixMilli(),
	}
	jm.jobs[id] = job
	return job
}

func (jm *JobManager) EnqueueJob(id string) int {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	if job, exists := jm.jobs[id]; exists {
		inQueue := false
		for _, qid := range jm.queue {
			if qid == id {
				inQueue = true
				break
			}
		}
		if !inQueue {
			jm.queue = append(jm.queue, id)
		}
		job.Status = "queued"
		job.QueuePosition = len(jm.queue)
		return job.QueuePosition
	}
	return 0
}

func (jm *JobManager) StartJob(id string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	newQueue := make([]string, 0, len(jm.queue))
	for _, qid := range jm.queue {
		if qid != id {
			newQueue = append(newQueue, qid)
		}
	}
	jm.queue = newQueue

	if job, exists := jm.jobs[id]; exists {
		job.Status = "uploading"
		job.QueuePosition = 0
	}
}

func (jm *JobManager) GetJob(id string) (*Job, bool) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	job, exists := jm.jobs[id]
	if !exists {
		return nil, false
	}

	copyJob := *job
	if copyJob.Status == "queued" {
		for i, qid := range jm.queue {
			if qid == id {
				copyJob.QueuePosition = i + 1
				break
			}
		}
	}
	return &copyJob, true
}

func (jm *JobManager) UpdateProgress(id string, uploaded, total int64, percent, speed float64) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	if job, exists := jm.jobs[id]; exists {
		job.Status = "uploading"
		job.QueuePosition = 0
		job.UploadedBytes = uploaded
		job.TotalBytes = total
		job.Progress = percent
		job.SpeedBytesPerSec = speed
	}
}

func (jm *JobManager) CompleteJob(id string, result any) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	newQueue := make([]string, 0, len(jm.queue))
	for _, qid := range jm.queue {
		if qid != id {
			newQueue = append(newQueue, qid)
		}
	}
	jm.queue = newQueue

	if job, exists := jm.jobs[id]; exists {
		job.Status = "completed"
		job.QueuePosition = 0
		job.Progress = 100
		job.Result = result
	}
}

func (jm *JobManager) FailJob(id string, err error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	newQueue := make([]string, 0, len(jm.queue))
	for _, qid := range jm.queue {
		if qid != id {
			newQueue = append(newQueue, qid)
		}
	}
	jm.queue = newQueue

	if job, exists := jm.jobs[id]; exists {
		job.Status = "failed"
		job.QueuePosition = 0
		job.Error = err.Error()
	}
}

func randomSuffix(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
