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

// EventResponseFromDomain converts a domain.Event + its ticket types
// into the API response DTO. D4.1 — the `ticket_types[]` slice is
// always materialised (Make over `nil`) so the JSON shape is stable
// even for an event with zero ticket_types: clients can rely on the
// key being present and iterate without nil-checks.
func EventResponseFromDomain(e domain.Event, ticketTypes []domain.TicketType) EventResponse {
	tts := make([]TicketTypeResponse, len(ticketTypes))
	for i, t := range ticketTypes {
		tts[i] = TicketTypeResponseFromDomain(t)
	}
	return EventResponse{
		ID:               e.ID(),
		Name:             e.Name(),
		TotalTickets:     e.TotalTickets(),
		AvailableTickets: e.AvailableTickets(),
		Version:          e.Version(),
		TicketTypes:      tts,
	}
}

// TicketTypeResponseFromDomain converts a single domain.TicketType
// into the wire shape. Optional fields (sale window / per-user limit
// / area_label) emit nil / empty-string so omitempty hides them in
// the rendered JSON when unset.
func TicketTypeResponseFromDomain(t domain.TicketType) TicketTypeResponse {
	return TicketTypeResponse{
		ID:               t.ID(),
		EventID:          t.EventID(),
		Name:             t.Name(),
		PriceCents:       t.PriceCents(),
		Currency:         t.Currency(),
		TotalTickets:     t.TotalTickets(),
		AvailableTickets: t.AvailableTickets(),
		SaleStartsAt:     t.SaleStartsAt(),
		SaleEndsAt:       t.SaleEndsAt(),
		PerUserLimit:     t.PerUserLimit(),
		AreaLabel:        t.AreaLabel(),
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
