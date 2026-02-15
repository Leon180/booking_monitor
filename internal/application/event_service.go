package application

import (
	"context"
	"fmt"

	"booking_monitor/internal/domain"
)

type EventService interface {
	CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error)
}

type eventService struct {
	repo domain.EventRepository
}

func NewEventService(repo domain.EventRepository) EventService {
	return &eventService{repo: repo}
}

func (s *eventService) CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error) {
	if totalTickets <= 0 {
		return nil, fmt.Errorf("total tickets must be positive")
	}

	event := &domain.Event{
		Name:             name,
		TotalTickets:     totalTickets,
		AvailableTickets: totalTickets,
		Version:          0,
	}

	if err := s.repo.Create(ctx, event); err != nil {
		return nil, err
	}

	return event, nil
}
