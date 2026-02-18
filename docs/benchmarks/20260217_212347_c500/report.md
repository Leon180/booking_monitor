# Benchmark Report: 500 Concurrent Users
**Date**: Tue Feb 17 21:23:47 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | 96d8f51 |
| **Parameters** | VUS=500, DURATION=60s |
| **Database** | PostgreSQL 15.15 on aarch64-unknown-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit |

## 2. Execution Log
```
docker run --rm -i --network=booking_monitor_default -e VUS=500 -e DURATION=60s -v /Users/lileon/project/booking_monitor/scripts/k6_load.js:/script.js grafana/k6 run /script.js

         /\      Grafana   /‾‾/  
    /\  /  \     |\  __   /  /   
   /  \/    \    | |/ /  /   ‾‾\ 
  /          \   |   (  |  (‾)  |
 / __________ \  |_|\_\  \_____/ 


     execution: local
        script: /script.js
        output: -

     scenarios: (100.00%) 1 scenario, 500 max VUs, 1m30s max duration (incl. graceful stop):
              * booking_stress: 500 looping VUs for 1m0s (gracefulStop: 30s)


Init             [  74% ] 368/500 VUs initialized
booking_stress   [   0% ]

running (0m00.8s), 500/500 VUs, 613 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  0m00.7s/1m0s

running (0m01.8s), 500/500 VUs, 3572 complete and 0 interrupted iterations
booking_stress   [   3% ] 500 VUs  0m01.7s/1m0s

running (0m02.8s), 500/500 VUs, 7008 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  0m02.7s/1m0s

running (0m03.8s), 500/500 VUs, 8869 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.7s/1m0s

running (0m04.8s), 500/500 VUs, 9996 complete and 0 interrupted iterations
booking_stress   [   8% ] 500 VUs  0m04.7s/1m0s

running (0m05.8s), 500/500 VUs, 13088 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.7s/1m0s

running (0m06.9s), 500/500 VUs, 13716 complete and 0 interrupted iterations
booking_stress   [  12% ] 500 VUs  0m07.3s/1m0s

running (0m07.8s), 500/500 VUs, 13956 complete and 0 interrupted iterations
booking_stress   [  13% ] 500 VUs  0m07.7s/1m0s

running (0m08.8s), 500/500 VUs, 16536 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  0m08.7s/1m0s

running (0m09.8s), 500/500 VUs, 20086 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.7s/1m0s

running (0m10.8s), 500/500 VUs, 25969 complete and 0 interrupted iterations
booking_stress   [  18% ] 500 VUs  0m10.7s/1m0s

running (0m11.8s), 500/500 VUs, 31237 complete and 0 interrupted iterations
booking_stress   [  19% ] 500 VUs  0m11.7s/1m0s

running (0m12.8s), 500/500 VUs, 35412 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.7s/1m0s

running (0m13.8s), 500/500 VUs, 38599 complete and 0 interrupted iterations
booking_stress   [  23% ] 500 VUs  0m13.7s/1m0s

running (0m14.8s), 500/500 VUs, 41961 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.7s/1m0s

running (0m16.1s), 500/500 VUs, 45038 complete and 0 interrupted iterations
booking_stress   [  27% ] 500 VUs  0m16.1s/1m0s

running (0m16.8s), 500/500 VUs, 45331 complete and 0 interrupted iterations
booking_stress   [  28% ] 500 VUs  0m16.7s/1m0s

running (0m17.8s), 500/500 VUs, 48887 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.7s/1m0s

running (0m18.8s), 500/500 VUs, 50998 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.7s/1m0s

running (0m19.8s), 500/500 VUs, 54156 complete and 0 interrupted iterations
booking_stress   [  33% ] 500 VUs  0m19.7s/1m0s

running (0m20.8s), 500/500 VUs, 58549 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.7s/1m0s

running (0m21.8s), 500/500 VUs, 62916 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.7s/1m0s

running (0m22.8s), 500/500 VUs, 66090 complete and 0 interrupted iterations
booking_stress   [  38% ] 500 VUs  0m22.7s/1m0s

running (0m23.8s), 500/500 VUs, 68587 complete and 0 interrupted iterations
booking_stress   [  40% ] 500 VUs  0m23.7s/1m0s

running (0m24.8s), 500/500 VUs, 71391 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  0m24.7s/1m0s

running (0m25.8s), 500/500 VUs, 73439 complete and 0 interrupted iterations
booking_stress   [  43% ] 500 VUs  0m25.7s/1m0s

running (0m26.8s), 500/500 VUs, 75602 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.7s/1m0s

running (0m27.8s), 500/500 VUs, 79916 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.7s/1m0s

running (0m28.8s), 500/500 VUs, 82109 complete and 0 interrupted iterations
booking_stress   [  48% ] 500 VUs  0m28.7s/1m0s

running (0m29.9s), 500/500 VUs, 82350 complete and 0 interrupted iterations
booking_stress   [  50% ] 500 VUs  0m30.3s/1m0s

running (0m31.3s), 500/500 VUs, 82830 complete and 0 interrupted iterations
booking_stress   [  52% ] 500 VUs  0m31.2s/1m0s

running (0m31.8s), 500/500 VUs, 84667 complete and 0 interrupted iterations
booking_stress   [  53% ] 500 VUs  0m31.8s/1m0s

running (0m32.8s), 500/500 VUs, 88077 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.7s/1m0s

running (0m33.8s), 500/500 VUs, 90429 complete and 0 interrupted iterations
booking_stress   [  56% ] 500 VUs  0m33.7s/1m0s

running (0m34.8s), 500/500 VUs, 95669 complete and 0 interrupted iterations
booking_stress   [  58% ] 500 VUs  0m34.7s/1m0s

running (0m35.8s), 500/500 VUs, 97371 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.7s/1m0s

running (0m36.8s), 500/500 VUs, 99551 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.7s/1m0s

running (0m37.8s), 500/500 VUs, 104858 complete and 0 interrupted iterations
booking_stress   [  63% ] 500 VUs  0m37.7s/1m0s

running (0m38.9s), 500/500 VUs, 109552 complete and 0 interrupted iterations
booking_stress   [  65% ] 500 VUs  0m38.8s/1m0s

running (0m39.8s), 500/500 VUs, 113668 complete and 0 interrupted iterations
booking_stress   [  66% ] 500 VUs  0m39.7s/1m0s

running (0m40.8s), 500/500 VUs, 119682 complete and 0 interrupted iterations
booking_stress   [  68% ] 500 VUs  0m40.7s/1m0s

running (0m41.8s), 500/500 VUs, 124556 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.7s/1m0s

running (0m42.8s), 500/500 VUs, 129584 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  0m42.7s/1m0s

running (0m43.8s), 500/500 VUs, 133627 complete and 0 interrupted iterations
booking_stress   [  73% ] 500 VUs  0m43.7s/1m0s

running (0m44.8s), 500/500 VUs, 138728 complete and 0 interrupted iterations
booking_stress   [  75% ] 500 VUs  0m44.7s/1m0s

running (0m45.8s), 500/500 VUs, 141767 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.7s/1m0s

running (0m46.8s), 500/500 VUs, 144993 complete and 0 interrupted iterations
booking_stress   [  78% ] 500 VUs  0m46.7s/1m0s

running (0m47.8s), 500/500 VUs, 147600 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.7s/1m0s

running (0m48.8s), 500/500 VUs, 150264 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  0m48.7s/1m0s

running (0m49.8s), 500/500 VUs, 152083 complete and 0 interrupted iterations
booking_stress   [  83% ] 500 VUs  0m49.7s/1m0s

running (0m50.8s), 500/500 VUs, 153082 complete and 0 interrupted iterations
booking_stress   [  85% ] 500 VUs  0m50.9s/1m0s

running (0m51.8s), 500/500 VUs, 156283 complete and 0 interrupted iterations
booking_stress   [  86% ] 500 VUs  0m51.7s/1m0s

running (0m52.8s), 500/500 VUs, 162197 complete and 0 interrupted iterations
booking_stress   [  88% ] 500 VUs  0m52.7s/1m0s

running (0m53.8s), 500/500 VUs, 167204 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.7s/1m0s

running (0m54.8s), 500/500 VUs, 174246 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.7s/1m0s

running (0m55.8s), 500/500 VUs, 178893 complete and 0 interrupted iterations
booking_stress   [  93% ] 500 VUs  0m55.7s/1m0s

running (0m56.8s), 500/500 VUs, 182751 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.7s/1m0s

running (0m57.8s), 500/500 VUs, 189423 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.7s/1m0s

running (0m58.8s), 500/500 VUs, 201296 complete and 0 interrupted iterations
booking_stress   [  98% ] 500 VUs  0m58.7s/1m0s

running (0m59.8s), 500/500 VUs, 210979 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.7s/1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✗ 'p(95)<200' p(95)=305.34ms


  █ TOTAL RESULTS 

    checks_total.......: 648184 10780.663998/s
    checks_succeeded...: 65.12% 422123 out of 648184
    checks_failed......: 34.87% 226061 out of 648184

    ✓ setup event created
    ✓ status is 200 or 409
    ✗ is sold out
      ↳  0% — ✓ 0 / ✗ 216061
    ✗ is duplicate
      ↳  95% — ✓ 206061 / ✗ 10000

    CUSTOM
    business_errors................: 0.00%  0 out of 216061

    HTTP
    http_req_duration..............: avg=106.02ms min=144.08µs med=71.72ms  max=2.8s  p(90)=222.5ms  p(95)=305.34ms
      { expected_response:true }...: avg=136.28ms min=1.25ms   med=111.11ms max=1.64s p(90)=244.81ms p(95)=303.97ms
    http_req_failed................: 95.37% 206061 out of 216062
    http_reqs......................: 216062 3593.565754/s

    EXECUTION
    iteration_duration.............: avg=137.32ms min=327.5µs  med=85.35ms  max=3s    p(90)=285.36ms p(95)=398.97ms
    iterations.....................: 216061 3593.549122/s
    vus............................: 500    min=0                max=500
    vus_max........................: 500    min=380              max=500

    NETWORK
    data_received..................: 48 MB  799 kB/s
    data_sent......................: 37 MB  611 kB/s




running (1m00.1s), 000/500 VUs, 216061 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
time="2026-02-18T05:24:53Z" level=error msg="thresholds on metrics 'http_req_duration' have been crossed"
make[1]: *** [stress-k6] Error 99
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 77.01% | 56.67MiB / 3.827GiB |
| **booking_redis** | 73.04% | 15.35MiB / 3.827GiB |
| **booking_app** | 290.67% | 98.15MiB / 3.827GiB |

❌ **Result**: FAIL
