package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"miniio_s3/config"
	"miniio_s3/db"
	"miniio_s3/handlers"
	"miniio_s3/middleware"
	"miniio_s3/service"
	"miniio_s3/storage"
	"miniio_s3/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Connect(cfg.DSN())
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()
	log.Println("[INFO] database connected and migrations applied")

	// Object Storage
	minioStore, err := storage.NewMinIOStorage(
		cfg.StorageEndpoint,
		cfg.StorageAccessKey,
		cfg.StorageSecretKey,
		cfg.StorageBucket,
		cfg.StorageUseSSL,
		cfg.PresignedURLMinutes,
	)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	log.Printf("[INFO] storage connected (bucket: %s)", cfg.StorageBucket)

	// Metadata stores
	metaStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	// Asynq client (task queue)
	asynqClient := worker.NewClient(cfg.RedisAddr, cfg.RedisPassword)
	defer asynqClient.Close()

	// Service layer
	fileSvc := service.New(minioStore, metaStore)
	enqueuer := service.NewAsynqEnqueuer(asynqClient)
	docSvc := service.NewDocumentService(minioStore, docStore, enqueuer)

	// HTTP handlers
	authHandler := handlers.NewAuthHandler(metaStore, cfg.JWTSecret, cfg.JWTExpiryHours, cfg.DefaultQuotaBytes)
	fileHandler := handlers.NewFileHandler(fileSvc)
	docHandler := handlers.NewDocumentHandler(docSvc)

	// Background worker
	processor := worker.NewProcessor(minioStore, docStore, asynqClient)
	go worker.StartWorker(worker.ServerConfig{
		RedisAddr:     cfg.RedisAddr,
		RedisPassword: cfg.RedisPassword,
		Concurrency:   cfg.WorkerConcurrency,
	}, processor)

	// Router
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Health check (no auth)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Auth routes (no JWT required)
	auth := r.Group("/auth")
	{
		auth.POST("/register", authHandler.Register)
		auth.POST("/login", authHandler.Login)
	}

	// Generic file routes (JWT required)
	files := r.Group("/files")
	files.Use(middleware.Auth(cfg.JWTSecret))
	{
		files.POST("/upload", fileHandler.Upload)
		files.GET("", fileHandler.List)
		files.GET("/:id", fileHandler.Get)
		files.GET("/:id/url", fileHandler.PresignedURL)
		files.DELETE("/:id", fileHandler.Delete)
	}

	// PDF document routes (JWT required)
	docs := r.Group("/documents")
	docs.Use(middleware.Auth(cfg.JWTSecret))
	{
		docs.POST("/upload", docHandler.Upload)
		docs.GET("", docHandler.ListDocuments)
		docs.GET("/:id", docHandler.GetDocument)
		docs.GET("/:id/pages/:page", docHandler.GetPage)
		docs.GET("/:id/pages", docHandler.GetPageRange)
		docs.POST("/:id/webhook", docHandler.RegisterWebhook)
	}

	// Server with graceful shutdown
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[INFO] server listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[INFO] shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("[INFO] server stopped")
}
