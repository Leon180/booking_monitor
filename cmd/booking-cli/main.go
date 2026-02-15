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
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/observability"
	postgresRepo "booking_monitor/internal/infrastructure/persistence/postgres"
	"booking_monitor/pkg/logger"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.uber.org/fx"
)

var (
	dbConnStr    string
	serverPort   string
	concurrency  int
	totalRequest int
)

func main() {
	var rootCmd = &cobra.Command{Use: "booking-cli"}

	var serverCmd = &cobra.Command{
		Use:   "server",
		Short: "Run the API server",
		Run:   runServer,
	}

	serverCmd.Flags().StringVar(&dbConnStr, "db", "postgres://user:password@localhost:5433/booking?sslmode=disable", "Database connection string")
	serverCmd.Flags().StringVar(&serverPort, "port", "8080", "Server port")

	var stressCmd = &cobra.Command{
		Use:   "stress",
		Short: "Run stress test",
		Run:   runStress,
	}
	stressCmd.Flags().IntVarP(&concurrency, "concurrency", "c", 1000, "Concurrency level")
	stressCmd.Flags().IntVarP(&totalRequest, "requests", "n", 2000, "Total requests")
	stressCmd.Flags().StringVar(&serverPort, "port", "8080", "Target server port")

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
	app := fx.New(
		// Provide Logger
		logger.Module,

		// Provide DB connection
		fx.Provide(func(log *zap.SugaredLogger) (*sql.DB, error) {
			db, err := sql.Open("postgres", dbConnStr)
			if err != nil {
				return nil, err
			}

			// Simple retry logic could be here, or use a robust opener
			for i := 0; i < 10; i++ {
				if err = db.Ping(); err == nil {
					break
				}
				time.Sleep(1 * time.Second)
			}

			db.SetMaxOpenConns(50)
			db.SetMaxIdleConns(50)
			return db, nil
		}),

		// Provide Modules
		postgresRepo.Module,
		cache.Module,
		application.Module,
		api.Module,

		// Run Server -> Invoke
		fx.Invoke(func(lc fx.Lifecycle, handler api.BookingHandler, log *zap.SugaredLogger) {
			tp := initTracer()

			// Hooks
			lc.Append(fx.Hook{
				OnStart: func(context.Context) error {
					r := gin.Default()
					r.Use(api.LoggerMiddleware(log)) // Inject Logger Middleware
					r.Use(observability.MetricsMiddleware())
					r.GET("/metrics", gin.WrapH(promhttp.Handler()))

					v1 := r.Group("/api/v1")
					api.RegisterRoutes(v1, handler)
					r.POST("/book", handler.HandleBook) // Legacy

					go func() {
						log.Infow("Starting server", "port", serverPort)
						if err := r.Run(":" + serverPort); err != nil {
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
	// ... (Implementation of stress test using flags) ...
	// Since the file content is large, I will simplify or include the logic.
	// For brevity in this tool call, I will include a shortened version that calls the actual logic.

	// Re-implementing the logic here inline or moving the file.
	// Let's implement it cleanly here.

	targetURL := fmt.Sprintf("http://localhost:%s/book", serverPort)
	fmt.Printf("Starting stress test: %d workers, %d requests, target: %s\n", concurrency, totalRequest, targetURL)

	// ... copy logic from stress_test/main.go ...
	// Actually, I should probably put this in `internal/application/stress_test.go` or similar,
	// but to keep it simple I'll put it here for now as requested.

	// Start the stress test
	startStressTest(concurrency, totalRequest, targetURL)
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
