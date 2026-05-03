package dto

import "booking_monitor/internal/domain"

// OrderResponseFromDomain converts a domain.Order into the API
// response DTO. Performs the in-memory copy that decouples wire
// contract from domain shape.
//
// D3: ReservedUntil is set to a *time.Time only when the domain Order
// carries a non-zero reservedUntil (Pattern A reservations). Legacy
// A4 rows pre-D3 have a zero reservedUntil and the field is omitted
// from the response (omitempty + nil pointer). This keeps the wire
// shape backwards-compatible: existing clients still parse the same
// JSON; new clients that branch on `if "reserved_until" in resp` can
// reliably distinguish Pattern A from legacy.
func OrderResponseFromDomain(o domain.Order) OrderResponse {
	resp := OrderResponse{
		ID:        o.ID(),
		EventID:   o.EventID(),
		UserID:    o.UserID(),
		Quantity:  o.Quantity(),
		Status:    string(o.Status()),
		CreatedAt: o.CreatedAt(),
	}
	if !o.ReservedUntil().IsZero() {
		ru := o.ReservedUntil()
		resp.ReservedUntil = &ru
	}
	return resp
}

// EventResponseFromDomain converts a domain.Event into the API
// response DTO.
func EventResponseFromDomain(e domain.Event) EventResponse {
	return EventResponse{
		ID:               e.ID(),
		Name:             e.Name(),
		TotalTickets:     e.TotalTickets(),
		AvailableTickets: e.AvailableTickets(),
		Version:          e.Version(),
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
