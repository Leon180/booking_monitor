# Kafka Integration

## docker-compose additions

```yaml
kafka:
  image: confluentinc/cp-kafka:7.6.0
  environment:
    KAFKA_BROKER_ID: 1
    KAFKA_ZOOKEEPER_CONNECT: zookeeper:2181
    KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://kafka:9092
    KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR: 1
  depends_on: [zookeeper]

zookeeper:
  image: confluentinc/cp-zookeeper:7.6.0
  environment:
    ZOOKEEPER_CLIENT_PORT: 2181
```

## Go Library

```
go get github.com/segmentio/kafka-go
```

## Outbox Relay Worker

The relay polls `events_outbox` for `status='PENDING'` rows and publishes to Kafka.

```go
// domain interface
type EventPublisher interface {
    Publish(ctx context.Context, topic string, payload []byte) error
}
```

Implementation in `internal/infrastructure/messaging/kafka_publisher.go`.

### Relay loop (application layer)

```go
func (r *outboxRelay) Run(ctx context.Context) {
    for {
        events, _ := r.outboxRepo.ListPending(ctx, 100)
        for _, e := range events {
            r.publisher.Publish(ctx, e.EventType, e.Payload)
            r.outboxRepo.MarkProcessed(ctx, e.ID)
        }
        time.Sleep(500 * time.Millisecond)
    }
}
```

## Topics

| Topic | Producer | Consumer |
|---|---|---|
| `order.created` | Outbox Relay | Payment Service |
| `order.paid` | Payment Service | Email / Analytics |

## Required DB changes

Add to `events_outbox`:
```sql
ALTER TABLE events_outbox ADD COLUMN processed_at TIMESTAMPTZ;
```

Add `ListPending` and `MarkProcessed` to `OutboxRepository` interface.
