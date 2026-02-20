# Future Robust Monolith Architecture (Phases 8-11)

This diagram illustrates the target architecture after implementing the stabilization phases (API Gateway, Distributed Locks, Sagas, DLQ).

```mermaid
graph TD
    Client((Client attackers/users))

    subgraph "Edge Protection (Phase 8)"
        Nginx["Nginx Reverse Proxy\n(Rate Limiter / Connection Draining)"]
    end

    subgraph "Scalable Modular Monolith (booking_app instances X 5)"
        direction TB
        API["Gin HTTP API"]
        BookingSvc["Booking Service"]
        WorkerPool[("Worker Pool w/ DLQ\n(Phase 11)")]
        OutboxRelay["Outbox Relay\n+ Distributed Lock (Phase 9)"]
        PaymentWorker["Payment Worker"]
        SagaHandler["Saga Compensator\n(Phase 10)"]
        
        API -->|Calls| BookingSvc
    end

    subgraph "Infrastructure"
        Redis[("Redis Cluster\n- Inventory (MAXLEN capped)\n- Lock Mutex")]
        Postgres[(PostgreSQL)]
        Kafka{{"Kafka Broker\n- order.created\n- order.failed"}}
    end

    %% Safe Flow Connections
    Client -->|HTTP requests| Nginx
    Nginx -->|Filtered Safe Traffic| API
    
    BookingSvc <-->|1. Fast Deduct| Redis
    Redis -.->|2. Async Stream| WorkerPool
    WorkerPool -->|3. Save Order| Postgres
    WorkerPool -.->|Fails 3x| DLQ((Dead Letter Queue))
    
    OutboxRelay <-->|Acquire Leadership| Redis
    OutboxRelay -->|Polls Outbox| Postgres
    OutboxRelay -->|Publish| Kafka
    
    Kafka -.->|Consume order.created| PaymentWorker
    PaymentWorker -->|Success: Update DB| Postgres
    PaymentWorker -->|Failure: Publish order.failed| Kafka
    
    Kafka -.->|Consume order.failed| SagaHandler
    SagaHandler -->|Compensating Transaction\n(INCRBY Inventory)| Redis

    classDef Secure fill:#d4edda,stroke:#28a745,stroke-width:2px;
    class Nginx Secure
    class OutboxRelay Secure
    class SagaHandler Secure
    class WorkerPool Secure
```
