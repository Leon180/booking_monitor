package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const baseURL = "http://localhost:8080/api/v1"

type CreateEventRequest struct {
	Name         string `json:"name"`
	TotalTickets int    `json:"total_tickets"`
}

type Event struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	TotalTickets     int    `json:"total_tickets"`
	AvailableTickets int    `json:"available_tickets"`
}

type BookRequest struct {
	UserID   int `json:"user_id"`
	EventID  int `json:"event_id"`
	Quantity int `json:"quantity"`
}

func main() {
	concurrency := flag.Int("c", 10, "Concurrency level")
	requests := flag.Int("n", 100, "Total requests")
	numEvents := flag.Int("events", 5, "Number of events to create")
	ticketsPerEvent := flag.Int("tickets", 1000, "Tickets per event")
	flag.Parse()

	fmt.Printf("Starting Stress Test\n")
	fmt.Printf("Concurrency: %d, Requests: %d\n", *concurrency, *requests)
	fmt.Printf("Events: %d, Tickets/Event: %d\n", *numEvents, *ticketsPerEvent)

	// Step 1: Create Events
	fmt.Println("\n[Setup] Creating events...")
	eventIDs := make([]int, 0, *numEvents)
	for i := 1; i <= *numEvents; i++ {
		evt, err := createEvent(fmt.Sprintf("Stress Test Event %d", i), *ticketsPerEvent)
		if err != nil {
			fmt.Printf("Failed to create event %d: %v\n", i, err)
			return
		}
		eventIDs = append(eventIDs, evt.ID)
		fmt.Printf("Created Event ID: %d\n", evt.ID)
	}

	// Step 2: Run Stress Test
	fmt.Println("\n[Attack] Starting load test...")
	start := time.Now()

	// Metrics
	var (
		successCounts [6]int64 // Index 1-5
		soldOutCounts [6]int64
		failCounts    [6]int64
	)

	var wg sync.WaitGroup
	requestsPerWorker := *requests / *concurrency

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			for j := 0; j < requestsPerWorker; j++ {
				eventID := eventIDs[r.Intn(len(eventIDs))]
				quantity := r.Intn(5) + 1
				userID := r.Intn(1000) + 1

				status, errMsg := bookTicket(userID, eventID, quantity)
				switch status {
				case http.StatusOK:
					atomic.AddInt64(&successCounts[quantity], 1)
				case http.StatusConflict: // Sold out
					atomic.AddInt64(&soldOutCounts[quantity], 1)
				default:
					atomic.AddInt64(&failCounts[quantity], 1)
					if atomic.LoadInt64(&failCounts[quantity]) <= 5 {
						fmt.Printf("Request Failed: Status %d, Error: %s\n", status, errMsg)
					}
				}
			}
		}()
	}

	wg.Wait()
	duration := time.Since(start)

	// Report
	var totalSuccess, totalSoldOut, totalFailed, totalTicketsSold int64
	for i := 1; i <= 5; i++ {
		totalSuccess += successCounts[i]
		totalSoldOut += soldOutCounts[i]
		totalFailed += failCounts[i]
		totalTicketsSold += successCounts[i] * int64(i)
	}

	fmt.Println("\n[Report] Test Complete")
	fmt.Printf("Duration: %v\n", duration)
	fmt.Printf("Requests/sec: %.2f\n", float64(*requests)/duration.Seconds())
	fmt.Printf("\nTotal Tickets Sold: %d\n", totalTicketsSold)

	fmt.Println("\n--- Breakdown ---")
	fmt.Printf("%-10s %-10s %-10s %-10s\n", "Quantity", "Success", "SoldOut", "Failed")
	for i := 1; i <= 5; i++ {
		fmt.Printf("%-10d %-10d %-10d %-10d\n", i, successCounts[i], soldOutCounts[i], failCounts[i])
	}
	fmt.Println("-----------------")
	fmt.Printf("%-10s %-10d %-10d %-10d\n", "Total", totalSuccess, totalSoldOut, totalFailed)
}

func createEvent(name string, totalTickets int) (*Event, error) {
	reqBody := CreateEventRequest{
		Name:         name,
		TotalTickets: totalTickets,
	}
	jsonBody, _ := json.Marshal(reqBody)

	resp, err := http.Post(baseURL+"/events", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var evt Event
	if err := json.NewDecoder(resp.Body).Decode(&evt); err != nil {
		return nil, err
	}
	return &evt, nil
}

func bookTicket(userID, eventID, quantity int) (int, string) {
	reqBody := BookRequest{
		UserID:   userID,
		EventID:  eventID,
		Quantity: quantity,
	}
	jsonBody, _ := json.Marshal(reqBody)

	resp, err := http.Post(baseURL+"/book", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
