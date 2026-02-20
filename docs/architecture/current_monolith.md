# Current System Architecture (Phase 7.7)

This diagram illustrates the current "Modular Monolith" architecture of the Booking Monitor System.

```mermaid
graph TD
    Client((Client))

    subgraph "Modular Monolith Process (booking_app)"
        direction TB
        API["Gin API Gateway\n(Idempotency Check)"]
        BookingSvc["Booking Service\n(Domain Logic)"]
        WorkerPool[("Worker Pool\n(Redis Consumer)")]
        OutboxRelay["Outbox Relay\n(DB Poller)"]
        PaymentWorker["Payment Worker\n(Kafka Consumer)"]
        
        API -->|Calls| BookingSvc
    end

    subgraph "Infrastructure"
        Redis[("Redis Cluster\n- Inventory Lua\n- Idempotency Cache\n- orders:stream")]
        Postgres[(PostgreSQL\n- orders table\n- events_outbox table)]
        Kafka{{"Kafka Broker\n(order.created)"}}
    end

    %% Flow Connections
    Client -->|HTTP POST /book| API
    BookingSvc <-->|1. Try Deduct & Append| Redis
    Redis -.->|2. Async Stream| WorkerPool
    WorkerPool -->|3. DB Transaction\n(Save Order + Outbox)| Postgres
    OutboxRelay -->|4. Polls events_outbox| Postgres
    OutboxRelay -->|5. Publish| Kafka
    Kafka -.->|6. Consume Topic| PaymentWorker
    PaymentWorker -->|7. Update Status to Paid| Postgres

    %% Highlighting SPOF Risks
    classDef SPOF stroke:#f00,stroke-width:2px;
    class OutboxRelay SPOF
    class API SPOF
    
    %% Note for SPOF
    note1>Red Border indicates current SPOF / Contention Risks when scaled horizontally]
    note1 -.-> OutboxRelay
    note1 -.-> API

```
