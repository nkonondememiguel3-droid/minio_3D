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

	metaStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)
	apiKeyStore := storage.NewAPIKeyStore(database)

	asynqClient := worker.NewClient(cfg.RedisAddr, cfg.RedisPassword)
	defer asynqClient.Close()

	fileSvc := service.New(minioStore, metaStore)
	enqueuer := service.NewAsynqEnqueuer(asynqClient)
	docSvc := service.NewDocumentService(minioStore, docStore, enqueuer)

	uploadLimiter := middleware.NewRateLimiter(cfg.RateLimitUploadsPerMinute, time.Minute)
	flexAuth := middleware.FlexAuth(cfg.JWTSecret, apiKeyStore)

	authHandler := handlers.NewAuthHandler(metaStore, cfg.JWTSecret, cfg.JWTExpiryHours, cfg.DefaultQuotaBytes)
	apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyStore)
	fileHandler := handlers.NewFileHandler(fileSvc)
	docHandler := handlers.NewDocumentHandler(docSvc)
	userHandler := handlers.NewUserHandler(metaStore, docStore)

	processor := worker.NewProcessor(minioStore, docStore, asynqClient)
	go worker.StartWorker(worker.ServerConfig{
		RedisAddr:     cfg.RedisAddr,
		RedisPassword: cfg.RedisPassword,
		Concurrency:   cfg.WorkerConcurrency,
	}, processor)

	watchdogCtx, watchdogCancel := context.WithCancel(context.Background())
	defer watchdogCancel()
	watchdog := worker.NewWatchdog(
		docStore,
		time.Duration(cfg.ProcessingTimeoutMinutes)*time.Minute,
		time.Duration(cfg.ProcessingTimeoutMinutes/2)*time.Minute,
	)
	go watchdog.Start(watchdogCtx)

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

	auth := r.Group("/auth")
	{
		auth.POST("/register", authHandler.Register)
		auth.POST("/login", authHandler.Login)

		// API key management — requires JWT or existing API key
		apiKeys := auth.Group("/api-keys")
		apiKeys.Use(flexAuth)
		{
			apiKeys.POST("", apiKeyHandler.Create)
			apiKeys.GET("", apiKeyHandler.List)
			apiKeys.DELETE("/:id", apiKeyHandler.Delete)
		}
	}

	users := r.Group("/users")
	users.Use(flexAuth)
	{
		users.GET("/me", userHandler.Me)
	}

	files := r.Group("/files")
	files.Use(flexAuth)
	{
		files.POST("/upload", fileHandler.Upload)
		files.GET("", fileHandler.List)
		files.GET("/:id", fileHandler.Get)
		files.GET("/:id/url", fileHandler.PresignedURL)
		files.DELETE("/:id", fileHandler.Delete)
	}

	docs := r.Group("/documents")
	docs.Use(flexAuth)
	{
		// Upload — rate limited separately
		docs.POST("/upload", uploadLimiter.Limit(), docHandler.Upload)

		docs.GET("", docHandler.ListDocuments)
		docs.GET("/:id", docHandler.GetDocument)
		docs.DELETE("/:id", docHandler.DeleteDocument)

		// Page retrieval — primary endpoint for backend consumers
		docs.GET("/:id/pages/all", docHandler.ListPages) // all pages + fresh URLs in one call
		docs.GET("/:id/pages/:page", docHandler.GetPage) // single page URL
		docs.GET("/:id/pages", docHandler.GetPageRange)  // ZIP download (start/end params)

		docs.POST("/:id/webhook", docHandler.RegisterWebhook)
	}

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

	watchdogCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("[INFO] server stopped")
}
