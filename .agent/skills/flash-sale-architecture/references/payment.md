# Payment Service

## Overview

A separate Go microservice that:
1. Consumes `order.created` from Kafka
2. Simulates/calls payment gateway
3. Updates `orders.status = 'paid'` in Postgres
4. Publishes `order.paid` to Kafka

## Project Structure

```
cmd/payment-service/
  main.go
internal/
  application/
    payment_service.go
  infrastructure/
    messaging/
      kafka_consumer.go
    persistence/
      postgres/
        order_repository.go  ← shared or duplicated from ticket service
```

## Domain Interface

```go
type PaymentGateway interface {
    Charge(ctx context.Context, orderID int, amount int) error
}
```

## Service Logic

```go
func (s *paymentService) ProcessOrder(ctx context.Context, msg OrderCreatedEvent) error {
    if err := s.gateway.Charge(ctx, msg.OrderID, msg.Amount); err != nil {
        // publish order.failed
        return err
    }
    if err := s.orderRepo.UpdateStatus(ctx, msg.OrderID, "paid"); err != nil {
        return err
    }
    return s.publisher.Publish(ctx, "order.paid", ...)
}
```

## Idempotency

Use `order_id` as Kafka message key. Consumer should check `orders.status` before processing to avoid double-charging on retry.

## docker-compose

Add as a separate service pointing to the same Postgres and Kafka:

```yaml
payment-service:
  build:
    context: .
    dockerfile: Dockerfile.payment
  environment:
    - DB_HOST=postgres
    - KAFKA_BROKERS=kafka:9092
  depends_on: [kafka, postgres]
```
