package dto

import "booking_monitor/internal/domain"

// OrderResponseFromDomain converts a domain.Order into the API
// response DTO. Performs the in-memory copy that decouples wire
// contract from domain shape — adding a domain field doesn't
// automatically change the API, and adding a DTO field doesn't
// require a domain change.
func OrderResponseFromDomain(o domain.Order) OrderResponse {
	return OrderResponse{
		ID:        o.ID,
		EventID:   o.EventID,
		UserID:    o.UserID,
		Quantity:  o.Quantity,
		Status:    string(o.Status),
		CreatedAt: o.CreatedAt,
	}
}

// EventResponseFromDomain converts a domain.Event into the API
// response DTO.
func EventResponseFromDomain(e domain.Event) EventResponse {
	return EventResponse{
		ID:               e.ID,
		Name:             e.Name,
		TotalTickets:     e.TotalTickets,
		AvailableTickets: e.AvailableTickets,
		Version:          e.Version,
	}
}

// ListBookingsResponseFromDomain assembles the paginated history
// response from the domain-layer slice plus pagination context.
func ListBookingsResponseFromDomain(orders []domain.Order, total, page, size int) ListBookingsResponse {
	out := ListBookingsResponse{
		Data: make([]OrderResponse, len(orders)),
		Meta: Meta{Total: total, Page: page, Size: size},
	}
	for i, o := range orders {
		out.Data[i] = OrderResponseFromDomain(o)
	}
	return out
}
