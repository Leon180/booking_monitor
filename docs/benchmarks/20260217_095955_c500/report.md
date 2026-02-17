# Benchmark Report: 500 Concurrent Users
**Date**: Tue Feb 17 09:59:55 PST 2026

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


running (0m00.7s), 500/500 VUs, 446 complete and 0 interrupted iterations
booking_stress   [   2% ] 500 VUs  00.5s/30s

running (0m01.7s), 500/500 VUs, 5955 complete and 0 interrupted iterations
booking_stress   [   5% ] 500 VUs  01.5s/30s

running (0m02.7s), 500/500 VUs, 21060 complete and 0 interrupted iterations
booking_stress   [   8% ] 500 VUs  02.5s/30s

running (0m03.7s), 500/500 VUs, 31415 complete and 0 interrupted iterations
booking_stress   [  12% ] 500 VUs  03.5s/30s

running (0m04.7s), 500/500 VUs, 50753 complete and 0 interrupted iterations
booking_stress   [  15% ] 500 VUs  04.5s/30s

running (0m05.7s), 500/500 VUs, 68948 complete and 0 interrupted iterations
booking_stress   [  18% ] 500 VUs  05.5s/30s

running (0m06.7s), 500/500 VUs, 84308 complete and 0 interrupted iterations
booking_stress   [  22% ] 500 VUs  06.5s/30s

running (0m07.7s), 500/500 VUs, 91746 complete and 0 interrupted iterations
booking_stress   [  25% ] 500 VUs  07.5s/30s

running (0m08.7s), 500/500 VUs, 94686 complete and 0 interrupted iterations
booking_stress   [  28% ] 500 VUs  08.5s/30s

running (0m09.7s), 500/500 VUs, 95977 complete and 0 interrupted iterations
booking_stress   [  32% ] 500 VUs  09.5s/30s

running (0m10.8s), 500/500 VUs, 96424 complete and 0 interrupted iterations
booking_stress   [  35% ] 500 VUs  10.6s/30s

running (0m11.7s), 500/500 VUs, 98344 complete and 0 interrupted iterations
booking_stress   [  38% ] 500 VUs  11.5s/30s

running (0m12.8s), 500/500 VUs, 101304 complete and 0 interrupted iterations
booking_stress   [  42% ] 500 VUs  12.5s/30s

running (0m13.7s), 500/500 VUs, 104259 complete and 0 interrupted iterations
booking_stress   [  45% ] 500 VUs  13.5s/30s

running (0m14.7s), 500/500 VUs, 111075 complete and 0 interrupted iterations
booking_stress   [  48% ] 500 VUs  14.5s/30s

running (0m15.8s), 500/500 VUs, 117329 complete and 0 interrupted iterations
booking_stress   [  52% ] 500 VUs  15.6s/30s

running (0m16.7s), 500/500 VUs, 122937 complete and 0 interrupted iterations
booking_stress   [  55% ] 500 VUs  16.5s/30s

running (0m17.7s), 500/500 VUs, 132347 complete and 0 interrupted iterations
booking_stress   [  58% ] 500 VUs  17.5s/30s

running (0m18.7s), 500/500 VUs, 142057 complete and 0 interrupted iterations
booking_stress   [  62% ] 500 VUs  18.5s/30s

running (0m19.7s), 500/500 VUs, 152788 complete and 0 interrupted iterations
booking_stress   [  65% ] 500 VUs  19.5s/30s

running (0m20.7s), 500/500 VUs, 159993 complete and 0 interrupted iterations
booking_stress   [  68% ] 500 VUs  20.5s/30s

running (0m21.7s), 500/500 VUs, 166294 complete and 0 interrupted iterations
booking_stress   [  72% ] 500 VUs  21.5s/30s

running (0m22.7s), 500/500 VUs, 169487 complete and 0 interrupted iterations
booking_stress   [  75% ] 500 VUs  22.5s/30s

running (0m23.7s), 500/500 VUs, 177940 complete and 0 interrupted iterations
booking_stress   [  78% ] 500 VUs  23.5s/30s

running (0m24.7s), 500/500 VUs, 183255 complete and 0 interrupted iterations
booking_stress   [  82% ] 500 VUs  24.5s/30s

running (0m25.7s), 500/500 VUs, 192007 complete and 0 interrupted iterations
booking_stress   [  85% ] 500 VUs  25.5s/30s

running (0m26.7s), 500/500 VUs, 198362 complete and 0 interrupted iterations
booking_stress   [  88% ] 500 VUs  26.5s/30s

running (0m27.7s), 500/500 VUs, 207880 complete and 0 interrupted iterations
booking_stress   [  92% ] 500 VUs  27.5s/30s

running (0m28.7s), 500/500 VUs, 216363 complete and 0 interrupted iterations
booking_stress   [  95% ] 500 VUs  28.5s/30s

running (0m29.7s), 500/500 VUs, 224976 complete and 0 interrupted iterations
booking_stress   [  98% ] 500 VUs  29.5s/30s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=137.24ms


  █ TOTAL RESULTS 

    checks_total.......: 689032 22725.858517/s
    checks_succeeded...: 65.21% 449355 out of 689032
    checks_failed......: 34.78% 239677 out of 689032

    ✓ setup event created
    ✓ status is 200 or 409
    ✗ is sold out
      ↳  0% — ✓ 0 / ✗ 229677
    ✗ is duplicate
      ↳  95% — ✓ 219677 / ✗ 10000

    CUSTOM
    business_errors................: 0.00%  0 out of 229677

    HTTP
    http_req_duration..............: avg=40.41ms min=108.29µs med=23.51ms max=1.07s    p(90)=96.48ms p(95)=137.24ms
      { expected_response:true }...: avg=39.25ms min=166.79µs med=16.93ms max=476.05ms p(90)=110.1ms p(95)=175.37ms
    http_req_failed................: 95.64% 219677 out of 229678
    http_reqs......................: 229678 7575.30816/s

    EXECUTION
    iteration_duration.............: avg=64.31ms min=157.7µs  med=36.3ms  max=2.1s     p(90)=140.8ms p(95)=203.34ms
    iterations.....................: 229677 7575.275178/s
    vus............................: 500    min=500              max=500
    vus_max........................: 500    min=500              max=500

    NETWORK
    data_received..................: 51 MB  1.7 MB/s
    data_sent......................: 39 MB  1.3 MB/s




running (0m30.3s), 000/500 VUs, 229677 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  30s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 71.97% | 90MiB / 3.827GiB |
| **booking_redis** | 79.91% | 24.89MiB / 3.827GiB |
| **booking_app** | 334.28% | 80.86MiB / 3.827GiB |

✅ **Result**: PASS
