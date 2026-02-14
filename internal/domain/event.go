package domain

import (
	"context"
	"errors"
)

var (
	ErrEventNotFound = errors.New("event not found")
	ErrSoldOut       = errors.New("event sold out")
)

type Event struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	TotalTickets     int    `json:"total_tickets"`
	AvailableTickets int    `json:"available_tickets"`
	Version          int    `json:"version"` // For optimistic locking
}

type EventRepository interface {
	GetByID(ctx context.Context, id int) (*Event, error)
	// DeductInventory reduces available tickets by quantity.
	// Returns ErrSoldOut if no tickets available.
	DeductInventory(ctx context.Context, eventID, quantity int) error
}
