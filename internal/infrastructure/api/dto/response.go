package dto

import (
	"time"

	"github.com/google/uuid"
)

// OrderResponse is the wire shape of an order in API responses. It is
// deliberately distinct from domain.Order — fields and types here
// reflect the JSON contract clients expect, not the domain model
// shape, so the two evolve independently.
//
// uuid.UUID marshals as RFC 4122 string ("01900000-...") via its
// built-in MarshalJSON. Clients receive ID + EventID as strings.
type OrderResponse struct {
	ID        uuid.UUID `json:"id"`
	EventID   uuid.UUID `json:"event_id"`
	UserID    int       `json:"user_id"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// EventResponse is the wire shape of an event in API responses.
type EventResponse struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	TotalTickets     int       `json:"total_tickets"`
	AvailableTickets int       `json:"available_tickets"`
	Version          int       `json:"version"`
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
