# Benchmark Report: 500 Concurrent Users
**Date**: Sun Feb 15 23:26:13 PST 2026

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


running (0m00.5s), 500/500 VUs, 29 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  00.2s/30s

running (0m01.4s), 500/500 VUs, 4171 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  01.2s/30s

running (0m02.6s), 500/500 VUs, 7153 complete and 0 interrupted iterations
booking_stress   [   8% ] 500 VUs  02.4s/30s

running (0m03.5s), 500/500 VUs, 12143 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  03.2s/30s

running (0m04.4s), 500/500 VUs, 26360 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  04.2s/30s

running (0m05.5s), 500/500 VUs, 42334 complete and 0 interrupted iterations
booking_stress   [  17% ] 500 VUs  05.2s/30s

running (0m06.5s), 500/500 VUs, 61547 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  06.2s/30s

running (0m07.5s), 500/500 VUs, 76429 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  07.3s/30s

running (0m08.5s), 500/500 VUs, 78741 complete and 0 interrupted iterations
booking_stress   [  27% ] 500 VUs  08.2s/30s

running (0m09.5s), 500/500 VUs, 83778 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  09.2s/30s

running (0m10.4s), 500/500 VUs, 91874 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  10.2s/30s

running (0m11.5s), 500/500 VUs, 99849 complete and 0 interrupted iterations
booking_stress   [  37% ] 500 VUs  11.2s/30s

running (0m12.5s), 500/500 VUs, 105476 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  12.3s/30s

running (0m13.4s), 500/500 VUs, 114842 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  13.2s/30s

running (0m14.5s), 500/500 VUs, 126205 complete and 0 interrupted iterations
booking_stress   [  47% ] 500 VUs  14.2s/30s

running (0m15.5s), 500/500 VUs, 138651 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  15.3s/30s

running (0m16.4s), 500/500 VUs, 146074 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  16.2s/30s

running (0m17.4s), 500/500 VUs, 157745 complete and 0 interrupted iterations
booking_stress   [  57% ] 500 VUs  17.2s/30s

running (0m18.5s), 500/500 VUs, 168423 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  18.2s/30s

running (0m19.4s), 500/500 VUs, 175621 complete and 0 interrupted iterations
booking_stress   [  64% ] 500 VUs  19.2s/30s

running (0m20.5s), 500/500 VUs, 179919 complete and 0 interrupted iterations
booking_stress   [  68% ] 500 VUs  20.3s/30s

running (0m21.5s), 500/500 VUs, 186843 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  21.2s/30s

running (0m22.4s), 500/500 VUs, 197606 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  22.2s/30s

running (0m23.4s), 500/500 VUs, 204221 complete and 0 interrupted iterations
booking_stress   [  77% ] 500 VUs  23.2s/30s

running (0m24.4s), 500/500 VUs, 211295 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  24.2s/30s

running (0m25.4s), 500/500 VUs, 222638 complete and 0 interrupted iterations
booking_stress   [  84% ] 500 VUs  25.2s/30s

running (0m26.4s), 500/500 VUs, 224976 complete and 0 interrupted iterations
booking_stress   [  87% ] 500 VUs  26.2s/30s

running (0m27.4s), 500/500 VUs, 235195 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  27.2s/30s

running (0m28.5s), 500/500 VUs, 246313 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  28.3s/30s

running (0m29.5s), 500/500 VUs, 257030 complete and 0 interrupted iterations
booking_stress   [  98% ] 500 VUs  29.3s/30s

running (0m30.2s), 000/500 VUs, 261724 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  30s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=116.93ms


  █ TOTAL RESULTS 

    checks_total.......: 785173 25973.917603/s
    checks_succeeded...: 65.39% 513449 out of 785173
    checks_failed......: 34.60% 271724 out of 785173

    ✓ setup event created
    ✓ status is 200 or 409
    ✗ is sold out
      ↳  0% — ✓ 0 / ✗ 261724
    ✗ is duplicate
      ↳  96% — ✓ 251724 / ✗ 10000

    CUSTOM
    business_errors................: 0.00%  0 out of 261724

    HTTP
    http_req_duration..............: avg=36.98ms min=90.16µs  med=22.83ms max=520.25ms p(90)=85.11ms  p(95)=116.93ms
      { expected_response:true }...: avg=72.01ms min=144.95µs med=42.38ms max=499.28ms p(90)=174.77ms p(95)=255.7ms 
    http_req_failed................: 96.17% 251724 out of 261725
    http_reqs......................: 261725 8657.994588/s

    EXECUTION
    iteration_duration.............: avg=56.51ms min=150.2µs  med=36.08ms max=977.44ms p(90)=122.4ms  p(95)=170.2ms 
    iterations.....................: 261724 8657.961508/s
    vus............................: 500    min=500              max=500
    vus_max........................: 500    min=500              max=500

    NETWORK
    data_received..................: 44 MB  1.4 MB/s
    data_sent......................: 45 MB  1.5 MB/s




running (0m30.2s), 000/500 VUs, 261724 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  30s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 4.77% | 13.87MiB / 3.827GiB |
| **booking_redis** | 75.15% | 17MiB / 3.827GiB |
| **booking_app** | 267.10% | 66.62MiB / 3.827GiB |

✅ **Result**: PASS
