package worker

import (
	"context"
	"log"
	"time"

	"miniio_s3/storage"
)

// Watchdog periodically scans for documents stuck in "processing" and marks
// them failed. This prevents documents from sitting in limbo forever if the
// worker crashes mid-extraction or pdfcpu panics on a malformed PDF.
type Watchdog struct {
	docStore *storage.DocumentStore
	timeout  time.Duration
	interval time.Duration
}

// NewWatchdog constructs a Watchdog.
// timeout  — how long a document may stay in "processing" before being failed.
// interval — how often to run the scan (should be << timeout).
func NewWatchdog(docStore *storage.DocumentStore, timeout, interval time.Duration) *Watchdog {
	return &Watchdog{
		docStore: docStore,
		timeout:  timeout,
		interval: interval,
	}
}

// Start runs the watchdog loop until ctx is cancelled.
// Call in a goroutine from main.
func (w *Watchdog) Start(ctx context.Context) {
	log.Printf("[watchdog] started (timeout=%s, interval=%s)", w.timeout, w.interval)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[watchdog] stopped")
			return
		case <-ticker.C:
			w.sweep()
		}
	}
}

// sweep finds all documents older than w.timeout still in "processing"
// and marks them as failed.
func (w *Watchdog) sweep() {
	cutoff := time.Now().Add(-w.timeout)
	n, err := w.docStore.FailStuckDocuments(cutoff)
	if err != nil {
		log.Printf("[watchdog] sweep error: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[watchdog] marked %d stuck document(s) as failed", n)
	}
}
