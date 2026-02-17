# Benchmark Report: 500 Concurrent Users
**Date**: Tue Feb 17 09:06:39 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | fefa372 |
| **Parameters** | VUS=500, DURATION=30s |
| **Database** | PostgreSQL 15.15 on aarch64-unknown-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit |

## 2. Execution Log
```
docker run --rm -i --network=booking_monitor_default -e VUS=500 -e DURATION=30s -v /Users/lileon/project/booking_monitor/scripts/k6_load.js:/script.js grafana/k6 run /script.js

         /\      Grafana   /‾‾/  
    /\  /  \     |\  __   /  /   
   /  \/    \    | |/ /  /   ‾‾\ 
  /          \   |   (  |  (‾)  |
 / __________ \  |_|\_\  \_____/ 


     execution: local
        script: /script.js
        output: -

     scenarios: (100.00%) 1 scenario, 500 max VUs, 1m0s max duration (incl. graceful stop):
              * booking_stress: 500 looping VUs for 30s (gracefulStop: 30s)

time="2026-02-17T17:06:40Z" level=warning msg="Request Failed" error="Post \"http://app:8080/api/v1/events\": lookup app on 127.0.0.11:53: no such host"


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=0s


  █ TOTAL RESULTS 

    checks_total.......: 1       8.99192/s
    checks_succeeded...: 0.00%   0 out of 1
    checks_failed......: 100.00% 1 out of 1

    ✗ setup event created
      ↳  0% — ✓ 0 / ✗ 1

    CUSTOM
    business_errors.....: 0.00%   0 out of 0

    HTTP
    http_req_duration...: avg=0s min=0s med=0s max=0s p(90)=0s p(95)=0s
    http_req_failed.....: 100.00% 1 out of 1
    http_reqs...........: 1       8.99192/s

    NETWORK
    data_received.......: 0 B     0 B/s
    data_sent...........: 0 B     0 B/s




Run              [ 100% ] setup()
booking_stress   [   0% ]
time="2026-02-17T17:06:40Z" level=error msg="GoError: the body is null so we can't transform it to JSON - this likely was because of a request error getting the response\n\tat reflect.methodValueCall (native)\n\tat setup (file:///script.js:49:27(43))\n" hint="script exception"
make[1]: *** [stress-k6] Error 107
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** |  |  |
| **booking_redis** |  |  |
| **booking_app** |  |  |

❌ **Result**: FAIL
