package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
)

// Stress-test CLI flag defaults. Not in config because the stress
// binary is a one-off tool, not a server process reading config.
const (
	stressDefaultBaseURL = "http://localhost:8080"
	// stressDefaultEventID was an int=1 before PR 34. Post-UUID-migration
	// the operator MUST supply a real UUID v7 obtained via
	// POST /api/v1/events — there is no useful default.
	stressDefaultUserRangeMax = 10000
	stressClientMaxIdleConns  = 1000
	stressClientTimeout       = 10 * time.Second
)

// runStress is the `stress` subcommand entry: a one-shot load generator
// against POST /api/v1/book. Exits once the job queue drains — no fx,
// no lifecycle, not a server.
func runStress(cmd *cobra.Command, _ []string) {
	// cobra validates these flags at registration; the only failure mode
	// is "flag not defined", which is a compile-time impossibility here.
	concurrency, _ := cmd.Flags().GetInt("concurrency")
	totalRequests, _ := cmd.Flags().GetInt("requests")
	baseURL, _ := cmd.Flags().GetString("base-url")
	eventIDStr, _ := cmd.Flags().GetString("event-id")
	userRange, _ := cmd.Flags().GetInt("user-range")

	if eventIDStr == "" {
		fmt.Fprintln(os.Stderr, "stress: --event-id is required (UUID v7 — create one via POST /api/v1/events first)")
		os.Exit(1)
	}

	targetURL := strings.TrimRight(baseURL, "/") + apiV1Prefix + "/book"
	fmt.Printf("Starting stress test: %d workers, %d requests, target: %s (event=%s, user_range=%d)\n",
		concurrency, totalRequests, targetURL, eventIDStr, userRange)

	startStressTest(concurrency, totalRequests, targetURL, eventIDStr, userRange)
}

// startStressTest orchestrates the load burst. jobs is pre-filled +
// closed so workers can range to completion without an explicit "done"
// signal. startChan is a release barrier: every worker blocks on it
// and they all unblock together, which is what flash-sale traffic
// actually looks like at the wire.
func startStressTest(concurrency, totalRequests int, url string, eventID string, userRange int) {
	var successCount, failCount int64
	var wg sync.WaitGroup

	jobs := make(chan struct{}, totalRequests)
	for range totalRequests {
		jobs <- struct{}{}
	}
	close(jobs)

	startChan := make(chan struct{})

	wg.Add(concurrency)
	for range concurrency {
		go stressWorker(jobs, startChan, &wg, &successCount, &failCount, url, eventID, userRange)
	}

	// Sleep gives workers time to block on <-startChan so they start
	// flooding at approximately the same moment.
	time.Sleep(1 * time.Second)
	start := time.Now()
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

// stressWorker drains the jobs channel, firing POST /book for each.
// Blocks on startChan first so every worker fires its first request
// at approximately the same instant as its peers.
//
// eventID is the UUID v7 string supplied via --event-id; user_id is
// generated per request from a small int range to spread load across
// distinct users (the orders.user_id column is still INT — the
// UUID migration was scoped to internally-owned aggregates).
func stressWorker(
	jobs <-chan struct{},
	startChan <-chan struct{},
	wg *sync.WaitGroup,
	successCount, failCount *int64,
	url string,
	eventID string,
	userRange int,
) {
	defer wg.Done()
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        stressClientMaxIdleConns,
			MaxIdleConnsPerHost: stressClientMaxIdleConns,
		},
		Timeout: stressClientTimeout,
	}

	<-startChan

	for range jobs {
		// json.Marshal cannot fail for this fixed shape.
		reqBody, _ := json.Marshal(map[string]any{
			"user_id":  rand.IntN(userRange) + 1, //nolint:gosec // G404 — load-generator user spread, not security-sensitive
			"event_id": eventID,
			"quantity": 1,
		})

		resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqBody))
		if err != nil {
			atomic.AddInt64(failCount, 1)
			continue
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			fmt.Println("Error consumption body:", err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			atomic.AddInt64(successCount, 1)
		} else {
			atomic.AddInt64(failCount, 1)
		}
	}
}
