// Package dto holds the HTTP wire-format types — request bodies, query
// strings, and response bodies — that the API layer exchanges with
// clients. Keeping these in their own package decouples the wire
// contract from internal domain shape, so the API can evolve
// independently (versioned routes, additional fields, JSON casing
// changes) without rippling into the domain or persistence layers.
//
// Mappers (in mapper.go) translate between domain values and DTOs.
// Handlers (in api/handler.go) bind requests, invoke services with
// domain types, then map domain results back to DTOs for the response.
package dto

import (
	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// BookingRequest is the wire shape of POST /api/v1/book. EventID is
// a UUID v7 string in the JSON body (since PR 34 — domain entities
// use UUID identity); uuid.UUID's UnmarshalJSON handles parsing.
// `binding:"required"` checks for non-zero value: for uuid.UUID
// (which is [16]byte) the zero value is uuid.Nil, so a missing /
// empty event_id is correctly rejected at bind time.
type BookingRequest struct {
	UserID   int       `json:"user_id" binding:"required"`
	EventID  uuid.UUID `json:"event_id" binding:"required"`
	Quantity int       `json:"quantity" binding:"required,min=1,max=10"`
}

// CreateEventRequest is the wire shape of POST /api/v1/events.
type CreateEventRequest struct {
	Name         string `json:"name" binding:"required"`
	TotalTickets int    `json:"total_tickets" binding:"required,min=1"`
}

// ListBookingsQueryParams binds the query string of GET /api/v1/history.
// Status is a pointer so "absent" and "empty string" are
// distinguishable — the existing code path uses nil to mean "no
// status filter" and a non-nil value to mean "filter to this status".
type ListBookingsQueryParams struct {
	Page   int     `form:"page"`
	Size   int     `form:"size"`
	Status *string `form:"status"`
}

// StatusFilter returns the typed *domain.OrderStatus corresponding to
// the raw string filter, or nil when no filter was provided. Lives
// here (not in the handler) so the conversion lives close to the
// query-string definition.
func (q ListBookingsQueryParams) StatusFilter() *domain.OrderStatus {
	if q.Status == nil {
		return nil
	}
	s := domain.OrderStatus(*q.Status)
	return &s
}
