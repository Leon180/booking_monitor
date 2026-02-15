Performance Benchmarks
Stage 1: Direct Database Management (Postgres Locking)
Date: 2026-02-14 Mechanism: Direct DB Transaction with SELECT ... FOR UPDATE. Infrastructure: Docker Compose (Go App + Postgres 15). Configuration:

Docker: After warm up
App: 50 DB Connections Max
Load: 500 Concurrent Workers
Test Run: 1 Million Requests
Command: make stress-go C=500 N=1000000 EVENTS=1 TICKETS=1000

Summary
Total Requests: 1,000,000
Concurrency: 500
Duration: 3m 47s
Throughput: ~4,405 req/sec
Success Rate: 100% (Transactions processed correctly)
Error Rate: 0% (No connection/server failures)
Detailed Results
Metric	Count	Description
Success	338	Orders successfully placed
Sold Out	999,662	Correctly rejected due to inventory
Failed	0	Technical failures (5xx/Timeouts)
Ticket Breakdown
Quantity per Order	Success	Sold Out	Tickets Sold
1	65	199,421	65
2	65	199,676	130
3	85	199,999	255
4	65	199,973	260
5	58	200,593	290
TOTAL	338	999,662	1,000
Analysis
Correctness: The system sold exactly 1,000 tickets. No overselling occurred.
Stability: Zero technical failures with 500 concurrent workers sharing 50 DB connections proves the connection pooling and locking strategy is stable.
Bottle Neck: The database lock (SELECT ... FOR UPDATE) is the primary bottleneck, serialization ensures data integrity at the cost of concurrency (handled via queuing).
