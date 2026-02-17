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
	"booking_monitor/internal/infrastructure/api"
	"booking_monitor/internal/infrastructure/api/middleware"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	postgresRepo "booking_monitor/internal/infrastructure/persistence/postgres"
	"booking_monitor/pkg/logger"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

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

	rootCmd.AddCommand(serverCmd, stressCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func initTracer() *trace.TracerProvider {
	ctx := context.Background()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("booking-service"),
		),
	)
	if err != nil {
		log.Fatal(err)
	}

	conn, err := grpc.NewClient("localhost:4317", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to create gRPC connection to collector: %v", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		log.Fatalf("failed to create trace exporter: %v", err)
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

		// Provide DB connection using Config
		fx.Provide(func(cfg *config.Config, log *zap.SugaredLogger) (*sql.DB, error) {
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
		}),

		// Provide Modules
		postgresRepo.Module,
		cache.Module,
		application.Module,
		api.Module,

		// Run Server -> Invoke
		fx.Invoke(func(lc fx.Lifecycle, handler api.BookingHandler, log *zap.SugaredLogger, cfg *config.Config) {
			tp := initTracer()

			// Hooks
			lc.Append(fx.Hook{
				OnStart: func(context.Context) error {
					r := gin.New()        // Use New() to control middleware
					r.Use(gin.Recovery()) // Recovery from panics

					r.Use(api.LoggerMiddleware(log))            // 1. Inject Logger
					r.Use(middleware.CorrelationIDMiddleware()) // 2. Inject Correlation ID (Enriches Logger)

					r.Use(observability.MetricsMiddleware())
					r.GET("/metrics", gin.WrapH(promhttp.Handler()))

					v1 := r.Group("/api/v1")
					api.RegisterRoutes(v1, handler)
					r.POST("/book", handler.HandleBook) // Legacy

					go func() {
						log.Infow("Starting server", "port", cfg.Server.Port)
						if err := r.Run(":" + cfg.Server.Port); err != nil {
							log.Errorw("Server failed", "error", err)
						}
					}()
					return nil
				},
				OnStop: func(ctx context.Context) error {
					log.Info("Shutting down tracer provider")
					return tp.Shutdown(ctx)
				},
			})
		}),
	)

	app.Run()
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
					"user_id":  1,
					"event_id": 1,
					"quantity": rand.IntN(5) + 1,
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
