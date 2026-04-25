package dto

import "time"

// OrderResponse is the wire shape of an order in API responses. It is
// deliberately distinct from domain.Order — fields and types here
// reflect the JSON contract clients expect, not the domain model
// shape, so the two evolve independently.
type OrderResponse struct {
	ID        int       `json:"id"`
	EventID   int       `json:"event_id"`
	UserID    int       `json:"user_id"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// EventResponse is the wire shape of an event in API responses.
type EventResponse struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	TotalTickets     int    `json:"total_tickets"`
	AvailableTickets int    `json:"available_tickets"`
	Version          int    `json:"version"`
}

// ListBookingsResponse is the wire shape of GET /api/v1/history.
type ListBookingsResponse struct {
	Data []OrderResponse `json:"data"`
	Meta Meta            `json:"meta"`
}

// Meta is the pagination block shared by list responses.
type Meta struct {
	Total int `json:"total"`
	Page  int `json:"page"`
	Size  int `json:"size"`
}

// BookingSuccessResponse is the wire shape returned by POST /api/v1/book
// on success. Currently a single message; evolved here so future
// additions (idempotency status, booking_id, etc.) don't ripple.
type BookingSuccessResponse struct {
	Message string `json:"message"`
}

// ErrorResponse is the wire shape of every 4xx/5xx response. Centralising
// the shape stops handlers drifting into ad-hoc `gin.H{"error": ...}`
// literals that subtly disagree on field name or status semantics.
type ErrorResponse struct {
	Error string `json:"error"`
}
