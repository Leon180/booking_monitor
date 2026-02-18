# ADR 001: Async Queue Technology Selection

## Context
We need to decouple the high-throughput `POST /book` API (Producer) from the database write operation (Consumer) to handle 10k+ RPS.
Two candidates were considered: **Redis Streams** and **Apache Kafka**.

## Comparison

| Feature | Redis Streams | Apache Kafka |
| :--- | :--- | :--- |
| **Infrastructure** | **Existing** (Already running Redis for inventory) | **New** (Requires Zookeeper/KRaft + JVM Brokers) |
| **Complexity** | Low (Single binary, simple commands `XADD`) | High (Topic partitions, consumer groups rebalancing) |
| **Performance** | In-Memory (Sub-ms latency, high throughput) | Disk-based (Low latency, extreme throughput) |
| **Durability** | **Volatile** (Depends on AOF config. Risk of data loss on crash) | **High** (Persisted on disk, replicated) |
| **Retention** | Limited by Memory/Stream size | Configurable (Time/Size based) |
| **Client Lib** | `go-redis` (Standard) | `sarama` or `segmentio/kafka-go` (Complex) |
| **Ops Cost** | Negligible (Part of existing Redis) | Moderate (Heavy resource usage in Docker) |

## Recommendation for "Booking Monitor"

### We recommend: **Redis Streams**

**Reasoning:**
1.  **Simplicity**: We are running a single-node setup in Docker. Adding Kafka (and Zookeeper) would significantly increase resource usage and complexity for a local demo.
2.  **Performance**: Redis is already optimized for this workload. Network roundtrips are minimized by using the same connection pool.
3.  **Development Speed**: Implementing a Redis Stream consumer in Go is faster than setting up a robust Kafka Consumer Group.
4.  **Sufficiency**: For this specific use case (Ordering Buffer), the durability risk of Redis (with AOF enabled) is acceptable compared to the operational overhead of Kafka.

### Alternative (Enterprise Scale)
If the goal is to simulate a multi-team enterprise environment where the Booking Service and Order Service are completely separate deployments, **Kafka** is the standard choice. However, for a single "Booking Monitor" service, it is overkill.
