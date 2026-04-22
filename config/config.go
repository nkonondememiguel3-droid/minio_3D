package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all application configuration loaded from environment variables.
// Every field has a sensible default for local development.
type Config struct {
	// Server
	Port string

	// PostgreSQL
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	// MinIO / S3
	StorageEndpoint  string
	StorageAccessKey string
	StorageSecretKey string
	StorageBucket    string
	StorageUseSSL    bool

	// Auth
	JWTSecret          string
	JWTExpiryHours     int
	PresignedURLMinutes int

	// Quotas
	DefaultQuotaBytes int64

	// Redis (Asynq task queue)
	RedisAddr     string
	RedisPassword string

	// Worker
	WorkerConcurrency        int
	ProcessingTimeoutMinutes int // documents stuck in "processing" longer than this are marked failed

	// Rate limiting
	RateLimitUploadsPerMinute int // max document uploads per user per minute (0 = disabled)

	// Asynq web dashboard
	AsynqDashboardEnabled bool
	AsynqDashboardPort    string
}

// Load reads configuration from environment variables with fallback defaults.
func Load() (*Config, error) {
	cfg := &Config{
		Port: getEnv("PORT", "8080"),

		DBHost:     getEnv("DB_HOST", "localhost"),
		DBPort:     getEnv("DB_PORT", "5432"),
		DBUser:     getEnv("DB_USER", "storageuser"),
		DBPassword: getEnv("DB_PASSWORD", "storagepass"),
		DBName:     getEnv("DB_NAME", "storagedb"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		StorageEndpoint:  getEnv("STORAGE_ENDPOINT", "localhost:9000"),
		StorageAccessKey: getEnv("STORAGE_ACCESS_KEY", "minioadmin"),
		StorageSecretKey: getEnv("STORAGE_SECRET_KEY", "minioadmin"),
		StorageBucket:    getEnv("STORAGE_BUCKET", "documents"),
		StorageUseSSL:    getEnvBool("STORAGE_USE_SSL", false),

		JWTSecret:           getEnv("JWT_SECRET", "change-me-in-production"),
		JWTExpiryHours:      getEnvInt("JWT_EXPIRY_HOURS", 24),
		PresignedURLMinutes: getEnvInt("PRESIGNED_URL_MINUTES", 15),

		DefaultQuotaBytes: getEnvInt64("DEFAULT_QUOTA_BYTES", 10*1024*1024*1024), // 10 GB

		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),

		WorkerConcurrency:        getEnvInt("WORKER_CONCURRENCY", 10),
		ProcessingTimeoutMinutes: getEnvInt("PROCESSING_TIMEOUT_MINUTES", 30),

		RateLimitUploadsPerMinute: getEnvInt("RATE_LIMIT_UPLOADS_PER_MINUTE", 10),

		AsynqDashboardEnabled: getEnvBool("ASYNQ_DASHBOARD_ENABLED", true),
		AsynqDashboardPort:    getEnv("ASYNQ_DASHBOARD_PORT", "8081"),
	}

	if cfg.JWTSecret == "change-me-in-production" {
		fmt.Fprintln(os.Stderr, "[WARN] JWT_SECRET is using the default insecure value. Set it in production.")
	}

	return cfg, nil
}

// DSN returns a PostgreSQL connection string.
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode,
	)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
