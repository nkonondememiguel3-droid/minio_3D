package worker

import (
	"context"
	"log"

	"github.com/hibiken/asynq"
)

// ServerConfig holds worker tuning parameters.
type ServerConfig struct {
	RedisAddr     string
	RedisPassword string
	Concurrency   int // total goroutines across all queues
}

// StartWorker launches the Asynq worker server.
// This is blocking — call it in a goroutine from main.
func StartWorker(cfg ServerConfig, proc *Processor) {
	redisOpts := asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	}

	srv := asynq.NewServer(redisOpts, asynq.Config{
		Concurrency: cfg.Concurrency,
		Queues: map[string]int{
			"critical": 6, // webhook delivery (fast, low volume)
			"default":  3, // PDF extraction (slower, heavier)
			"low":      1, // future: cleanup jobs
		},
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			log.Printf("[worker] task %s failed: %v", task.Type(), err)
		}),
	})

	mux := asynq.NewServeMux()
	mux.HandleFunc(TypeExtractPages, proc.HandleExtractPages)
	mux.HandleFunc(TypeFireWebhook, proc.HandleFireWebhook)

	log.Printf("[worker] starting with concurrency=%d on %s", cfg.Concurrency, cfg.RedisAddr)
	if err := srv.Run(mux); err != nil {
		log.Fatalf("[worker] server error: %v", err)
	}
}

// NewClient returns an Asynq client for enqueueing tasks.
func NewClient(redisAddr, redisPassword string) *asynq.Client {
	return asynq.NewClient(asynq.RedisClientOpt{
		Addr:     redisAddr,
		Password: redisPassword,
	})
}
