// Background worker for preemptive summarization.
package preemptive

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// JobStatus represents the status of a summarization job.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Backward compatibility aliases.
const (
	JobStatusQueued    = JobQueued
	JobStatusRunning   = JobRunning
	JobStatusCompleted = JobCompleted
	JobStatusFailed    = JobFailed
	JobStatusCancelled = JobCancelled
)

// Job represents a background summarization job.
type Job struct {
	ID            string
	SessionID     string
	Status        JobStatus
	CreatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
	Messages      []json.RawMessage
	MessageCount  int
	Model         string
	Summary       string
	SummaryTokens int
	LastIndex     int
	Error         string
	done          chan struct{}
}

// SummarizationJob is an alias for backward compatibility.
type SummarizationJob = Job

// Worker handles background summarization jobs.
type Worker struct {
	summarizer       *Summarizer
	sessions         *SessionManager
	summarizerCfg    SummarizerConfig
	triggerThreshold float64

	jobs     map[string]*Job
	jobQueue chan *Job
	mu       sync.RWMutex
	running  bool
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewWorker creates a new background worker.
func NewWorker(summarizer *Summarizer, sessions *SessionManager, cfg SummarizerConfig, triggerThreshold float64) *Worker {
	return &Worker{
		summarizer:       summarizer,
		sessions:         sessions,
		summarizerCfg:    cfg,
		triggerThreshold: triggerThreshold,
		jobs:             make(map[string]*Job),
		jobQueue:         make(chan *Job, 100),
		stopChan:         make(chan struct{}),
	}
}

// Start starts background workers.
func (w *Worker) Start() {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()

	log.Info().Msg("Starting preemptive summarization workers")
	for i := 0; i < 2; i++ {
		w.wg.Add(1)
		go w.processJobs(i)
	}
}

// Stop stops all workers.
func (w *Worker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	close(w.stopChan)
	w.mu.Unlock()
	w.wg.Wait()
	log.Info().Msg("Preemptive summarization workers stopped")
}

// SubmitJob submits a new summarization job (legacy name).
func (w *Worker) SubmitJob(sessionID string, messages []json.RawMessage, model string) (*Job, error) {
	return w.Submit(sessionID, messages, model), nil
}

// Submit submits a new summarization job.
func (w *Worker) Submit(sessionID string, messages []json.RawMessage, model string) *Job {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Return existing job if in progress
	if existing, ok := w.jobs[sessionID]; ok {
		if existing.Status == JobQueued || existing.Status == JobRunning {
			return existing
		}
	}

	job := &Job{
		ID:           sessionID,
		SessionID:    sessionID,
		Status:       JobQueued,
		CreatedAt:    time.Now(),
		Messages:     messages,
		MessageCount: len(messages),
		Model:        model,
		done:         make(chan struct{}),
	}

	w.jobs[sessionID] = job

	select {
	case w.jobQueue <- job:
		log.Info().Str("session_id", sessionID).Int("messages", len(messages)).Msg("Summarization job queued")
	default:
		job.Status = JobFailed
		job.Error = "queue full"
		close(job.done)
	}

	return job
}

// GetJob retrieves a job by session ID.
func (w *Worker) GetJob(sessionID string) *Job {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.jobs[sessionID]
}

// WaitForJob waits for a job to complete with timeout.
func (w *Worker) WaitForJob(sessionID string, timeout time.Duration) bool {
	return w.Wait(sessionID, timeout)
}

// Wait waits for a job to complete with timeout.
func (w *Worker) Wait(sessionID string, timeout time.Duration) bool {
	w.mu.RLock()
	job, ok := w.jobs[sessionID]
	w.mu.RUnlock()

	if !ok {
		return false
	}

	select {
	case <-job.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (w *Worker) processJobs(workerID int) {
	defer w.wg.Done()

	for {
		select {
		case <-w.stopChan:
			return
		case job := <-w.jobQueue:
			w.processJob(workerID, job)
		}
	}
}

func (w *Worker) processJob(workerID int, job *Job) {
	startTime := time.Now()

	w.mu.Lock()
	job.Status = JobRunning
	job.StartedAt = &startTime
	w.mu.Unlock()

	log.Info().Int("worker", workerID).Str("session_id", job.SessionID).Int("messages", job.MessageCount).Msg("Processing summarization job")

	// Update session state
	_ = w.sessions.Update(job.SessionID, func(s *Session) {
		s.State = StatePending
		now := time.Now()
		s.SummaryTriggeredAt = &now
	})

	// Do summarization
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := w.summarizer.Summarize(ctx, SummarizeInput{
		Messages:         job.Messages,
		TriggerThreshold: w.triggerThreshold,
		KeepRecentTokens: w.summarizerCfg.KeepRecentTokens,
		KeepRecentCount:  w.summarizerCfg.KeepRecentCount,
		Model:            job.Model,
	})

	now := time.Now()

	w.mu.Lock()
	defer w.mu.Unlock()

	if err != nil {
		job.Status = JobFailed
		job.Error = err.Error()
		job.CompletedAt = &now
		_ = w.sessions.Update(job.SessionID, func(s *Session) { s.State = StateIdle })

		// Log skip (not an error) for "not enough content" cases
		if logger := GetCompactionLogger(); logger != nil {
			if strings.Contains(err.Error(), "not enough content to summarize") {
				log.Debug().Str("session_id", job.SessionID).Msg("Summarization skipped: not enough content")
				logger.LogSkip(job.SessionID, "preemptive", err.Error(), map[string]interface{}{"model": job.Model})
			} else {
				log.Error().Err(err).Str("session_id", job.SessionID).Msg("Summarization job failed")
				logger.LogError(job.SessionID, "preemptive", err, map[string]interface{}{"model": job.Model})
			}
		}
	} else {
		job.Status = JobCompleted
		job.CompletedAt = &now
		job.Summary = result.Summary
		job.SummaryTokens = result.SummaryTokens
		job.LastIndex = result.LastSummarizedIndex
		log.Info().Str("session_id", job.SessionID).Int("summary_tokens", result.SummaryTokens).Dur("duration", result.Duration).Msg("Summarization job completed")
		_ = w.sessions.SetSummaryReady(job.SessionID, result.Summary, result.SummaryTokens, result.LastSummarizedIndex, job.MessageCount)
		// Log preemptive complete
		if logger := GetCompactionLogger(); logger != nil {
			logger.LogPreemptiveComplete(job.SessionID, job.Model, result.LastSummarizedIndex+1, result.SummaryTokens, result.Duration, w.summarizerCfg.Provider, w.summarizerCfg.Model)
		}
	}

	close(job.done)
}

// Stats returns worker statistics.
func (w *Worker) Stats() map[string]interface{} {
	w.mu.RLock()
	defer w.mu.RUnlock()

	counts := make(map[JobStatus]int)
	for _, job := range w.jobs {
		counts[job.Status]++
	}

	return map[string]interface{}{
		"total_jobs":   len(w.jobs),
		"queue_length": len(w.jobQueue),
		"by_status":    counts,
		"running":      w.running,
	}
}
