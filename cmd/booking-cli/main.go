package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api"
	"booking_monitor/internal/infrastructure/api/middleware"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/messaging"
	"booking_monitor/internal/infrastructure/observability"
	postgresRepo "booking_monitor/internal/infrastructure/persistence/postgres"
	"booking_monitor/pkg/logger"

	paymentApp "booking_monitor/internal/application/payment"
	paymentInfra "booking_monitor/internal/infrastructure/payment"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

func main() {
	var rootCmd = &cobra.Command{Use: "booking-cli"}

	var serverCmd = &cobra.Command{
		Use:   "server",
		Short: "Run the API server",
		Run:   runServer,
	}

	var stressCmd = &cobra.Command{
		Use:   "stress",
		Short: "Run stress test",
		Run:   runStress,
	}

	// Flags for stress test (still useful to keep as flags since they are runtime args)
	stressCmd.Flags().IntP("concurrency", "c", 1000, "Concurrency level")
	stressCmd.Flags().IntP("requests", "n", 2000, "Total requests")
	stressCmd.Flags().String("port", "8080", "Target server port")

	var paymentCmd = &cobra.Command{
		Use:   "payment",
		Short: "Run the Payment Service worker",
		Run:   runPaymentWorker,
	}

	rootCmd.AddCommand(serverCmd, stressCmd, paymentCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runPaymentWorker(cmd *cobra.Command, args []string) {
	// 1. Load Config
	cfg, err := config.LoadConfig("config/config.yml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		// Provide Logger
		logger.Module,

		// Provide Config
		fx.Provide(func() *config.Config {
			return cfg
		}),

		// Provide DB connection using Shared Provider
		fx.Provide(provideDB),

		// Provide Modules
		postgresRepo.Module,

		// Payment Specific Providers
		fx.Provide(
			// Cast MockGateway to PaymentGateway interface
			fx.Annotate(
				paymentInfra.NewMockGateway,
				fx.As(new(domain.PaymentGateway)),
			),
			paymentApp.NewService, // domain.PaymentService
			// Kafka Consumer
			func(cfg *config.Config, logger *zap.SugaredLogger) *messaging.KafkaConsumer {
				return messaging.NewKafkaConsumer(&cfg.Kafka, logger)
			},
		),

		// Invoke Consumer Start
		fx.Invoke(func(lc fx.Lifecycle, consumer *messaging.KafkaConsumer, service domain.PaymentService, log *zap.SugaredLogger) {
			ctx, cancel := context.WithCancel(context.Background())

			lc.Append(fx.Hook{
				OnStart: func(context.Context) error {
					log.Info("Starting Payment Service Worker...")
					go func() {
						if err := consumer.Start(ctx, service); err != nil {
							log.Errorw("Consumer stopped with error", "error", err)
						}
					}()
					return nil
				},
				OnStop: func(ctx context.Context) error {
					log.Info("Stopping Payment Service Worker...")
					cancel()
					return consumer.Close()
				},
			})
		}),
	)

	app.Run()
}

func initTracer() *trace.TracerProvider {
	ctx := context.Background()

	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = "booking-service"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(svcName),
		),
	)
	if err != nil {
		log.Printf("failed to create resource: %v", err)
	}

	// Will automatically use OTEL_EXPORTER_OTLP_ENDPOINT environment variable.
	// If not set, defaults to localhost:4317. It handles http/https prefixes correctly.
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		log.Printf("failed to create trace exporter: %v", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithSampler(trace.AlwaysSample()),
		trace.WithResource(res),
		trace.WithBatcher(traceExporter),
	)
	otel.SetTracerProvider(tp)
	return tp
}

func runServer(cmd *cobra.Command, args []string) {
	// 1. Load Config
	cfg, err := config.LoadConfig("config/config.yml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		// Provide Logger
		logger.Module,

		// Provide Config
		fx.Provide(func() *config.Config {
			return cfg
		}),

		// Provide DB connection using Shared Provider
		fx.Provide(provideDB),

		// Provide Modules
		postgresRepo.Module,
		cache.Module,
		application.Module,
		api.Module,

		// Provide Kafka publisher (lifecycle-managed: Close() called on shutdown)
		messaging.Module,
		fx.Provide(func(cfg *config.Config) messaging.MessagingConfig {
			return messaging.MessagingConfig{
				Brokers:      messaging.ParseBrokers(cfg.Kafka.Brokers),
				WriteTimeout: cfg.Kafka.WriteTimeout,
			}
		}),
		// Provide OutboxBatchSize so Fx can inject it into NewOutboxRelay.
		fx.Provide(func(cfg *config.Config) int { return cfg.Kafka.OutboxBatchSize }),

		// Provide concrete implementations of application interfaces
		fx.Provide(observability.NewWorkerMetrics),
		fx.Provide(func(cfg *config.Config) *messaging.SagaConsumer {
			return messaging.NewSagaConsumer(&cfg.Kafka)
		}),

		// Run Server -> Invoke
		fx.Invoke(func(lc fx.Lifecycle, handler api.BookingHandler, log *zap.SugaredLogger, cfg *config.Config, worker application.WorkerService, sagaConsumer *messaging.SagaConsumer, compensator application.SagaCompensator) {
			tp := initTracer()

			// Context for worker shutdown
			workerCtx, workerCancel := context.WithCancel(context.Background())

			// Shared between OnStart and OnStop so the HTTP server can be
			// gracefully shut down. Constructed inside OnStart.
			var httpServer *http.Server

			// Hooks
			lc.Append(fx.Hook{
				OnStart: func(context.Context) error {
					r := gin.New()        // Use New() to control middleware
					r.Use(gin.Recovery()) // Recovery from panics

					// Secure ClientIP resolution behind Nginx Proxy (Docker default subnets)
					if err := r.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}); err != nil {
						log.Warnw("Failed to set trusted proxies", "error", err)
					}

					r.Use(api.LoggerMiddleware(log))            // 1. Inject Logger
					r.Use(middleware.CorrelationIDMiddleware()) // 2. Inject Correlation ID (Enriches Logger)

					r.Use(observability.MetricsMiddleware())
					r.GET("/metrics", gin.WrapH(promhttp.Handler()))

					v1 := r.Group("/api/v1")
					api.RegisterRoutes(v1, handler)
					r.POST("/book", handler.HandleBook) // Legacy

					// Construct http.Server explicitly so configured ReadTimeout /
					// WriteTimeout take effect. `r.Run()` wraps a default
					// http.Server which ignores these, leaving the process open
					// to slow-loris attacks.
					httpServer = &http.Server{
						Addr:              ":" + cfg.Server.Port,
						Handler:           r,
						ReadTimeout:       cfg.Server.ReadTimeout,
						ReadHeaderTimeout: cfg.Server.ReadTimeout,
						WriteTimeout:      cfg.Server.WriteTimeout,
						IdleTimeout:       2 * cfg.Server.WriteTimeout,
						MaxHeaderBytes:    1 << 20, // 1 MiB
					}

					go func() {
						log.Infow("Starting server",
							"port", cfg.Server.Port,
							"read_timeout", cfg.Server.ReadTimeout,
							"write_timeout", cfg.Server.WriteTimeout)
						if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
							log.Errorw("Server failed", "error", err)
						}
					}()

					// Start Background Worker (Redis -> DB Outbox)
					go worker.Start(workerCtx)

					// Start Saga Consumer (Kafka order.failed -> Restore DB/Redis)
					go func() {
						if err := sagaConsumer.Start(workerCtx, compensator); err != nil {
							log.Errorw("Saga consumer stopped with error", "error", err)
						}
					}()

					return nil
				},
				OnStop: func(ctx context.Context) error {
					log.Info("Stopping worker services...")
					workerCancel()
					_ = sagaConsumer.Close()

					if httpServer != nil {
						log.Info("Shutting down HTTP server")
						if err := httpServer.Shutdown(ctx); err != nil {
							log.Errorw("HTTP server shutdown error", "error", err)
						}
					}

					log.Info("Shutting down tracer provider")
					return tp.Shutdown(ctx)
				},
			})
		}),
	)

	app.Run()
}

// provideDB creates a database connection with retry logic.
func provideDB(cfg *config.Config, log *zap.SugaredLogger) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.Postgres.DSN)
	if err != nil {
		return nil, err
	}

	// Simple retry logic
	for i := 0; i < 10; i++ {
		if err = db.Ping(); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}

	db.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	db.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	db.SetConnMaxIdleTime(cfg.Postgres.MaxIdleTime)
	return db, nil
}

// Ported from stress_test/main.go
func runStress(cmd *cobra.Command, args []string) {
	concurrency, _ := cmd.Flags().GetInt("concurrency")
	totalRequests, _ := cmd.Flags().GetInt("requests")
	port, _ := cmd.Flags().GetString("port")

	targetURL := fmt.Sprintf("http://localhost:%s/book", port)
	fmt.Printf("Starting stress test: %d workers, %d requests, target: %s\n", concurrency, totalRequests, targetURL)

	// Start the stress test
	startStressTest(concurrency, totalRequests, targetURL)
}

func startStressTest(concurrency, totalRequests int, url string) {
	var successCount int64
	var failCount int64
	var wg sync.WaitGroup

	start := time.Now()

	jobs := make(chan struct{}, totalRequests)
	for i := 0; i < totalRequests; i++ {
		jobs <- struct{}{}
	}
	close(jobs)

	startChan := make(chan struct{})

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			client := &http.Client{
				Transport: &http.Transport{
					MaxIdleConns:        1000,
					MaxIdleConnsPerHost: 1000,
				},
				Timeout: 10 * time.Second,
			}

			<-startChan

			for range jobs {
				reqBody, _ := json.Marshal(map[string]int{
					"user_id":  rand.IntN(10000) + 1,
					"event_id": 1,
					"quantity": 1, // Fix quantity to 1 for easier calculation
				})

				resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqBody))
				if err != nil {
					atomic.AddInt64(&failCount, 1)
					continue
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					fmt.Println("Error consumption body:", err)
				}
				_ = resp.Body.Close()

				if resp.StatusCode == 200 {
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&failCount, 1)
				}
			}
		}()
	}

	time.Sleep(1 * time.Second)
	fmt.Println("Flooding...")
	close(startChan)

	wg.Wait()
	duration := time.Since(start)

	fmt.Printf("Completed in %v\n", duration)
	if duration.Seconds() > 0 {
		fmt.Printf("Requests per second: %.2f\n", float64(totalRequests)/duration.Seconds())
	}
	fmt.Printf("Success: %d\n", successCount)
	fmt.Printf("Failed: %d\n", failCount)
}
