package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter enforces a sliding-window rate limit per user.
// It is in-process and resets on restart — suitable for Phase 1.
// Phase 2: replace with a Redis-backed counter for multi-instance deployments.
type RateLimiter struct {
	mu       sync.Mutex
	windows  map[string][]time.Time // userID → timestamps of recent requests
	limit    int                    // max requests per window
	window   time.Duration          // rolling window size
}

// NewRateLimiter creates a per-user sliding-window rate limiter.
// limit=10, window=time.Minute → max 10 requests per user per minute.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
	// Background cleanup to prevent unbounded memory growth.
	go rl.cleanup()
	return rl
}

// Limit returns a Gin middleware that enforces the rate limit.
// It uses the user_id set by the Auth middleware — call after auth.
func (rl *RateLimiter) Limit() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := GetUserID(c)
		if userID == "" {
			// No user context — auth middleware will have already rejected this.
			c.Next()
			return
		}

		if !rl.allow(userID) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "upload rate limit exceeded — please slow down",
				"limit": rl.limit,
				"window_seconds": int(rl.window.Seconds()),
			})
			return
		}

		c.Next()
	}
}

// allow returns true if the user is within the rate limit and records the attempt.
func (rl *RateLimiter) allow(userID string) bool {
	now := time.Now()
	cutoff := now.Add(-rl.window)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Prune timestamps outside the window.
	timestamps := rl.windows[userID]
	valid := timestamps[:0]
	for _, t := range timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		rl.windows[userID] = valid
		return false
	}

	rl.windows[userID] = append(valid, now)
	return true
}

// cleanup runs every 5 minutes and removes entries for users with no recent activity.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-rl.window)
		rl.mu.Lock()
		for userID, timestamps := range rl.windows {
			active := false
			for _, t := range timestamps {
				if t.After(cutoff) {
					active = true
					break
				}
			}
			if !active {
				delete(rl.windows, userID)
			}
		}
		rl.mu.Unlock()
	}
}
