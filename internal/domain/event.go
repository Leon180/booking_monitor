package domain

import (
	"context"
	"errors"
	"fmt"
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
	Version          int    `json:"version"` // Added Version
}

func (e *Event) Deduct(quantity int) error {
	if quantity < 0 {
		return fmt.Errorf("invalid quantity")
	}
	if e.AvailableTickets < quantity {
		return ErrSoldOut
	}
	e.AvailableTickets -= quantity
	return nil
}

//go:generate mockgen -source=event.go -destination=../mocks/event_repository_mock.go -package=mocks
type EventRepository interface {
	Create(ctx context.Context, event *Event) error
	GetByID(ctx context.Context, id int) (*Event, error)
	DeductInventory(ctx context.Context, eventID, quantity int) error // Deprecated in favor of Lifecycle, but kept for legacy
	Update(ctx context.Context, event *Event) error
}
