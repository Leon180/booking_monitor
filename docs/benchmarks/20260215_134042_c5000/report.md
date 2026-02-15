# Benchmark Report: 5000 Concurrent Users
**Date**: Sun Feb 15 13:40:42 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | f9ff381 |
| **Parameters** | VUS=5000, DURATION=60s |
| **Database** | PostgreSQL 15.15 on aarch64-unknown-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit |

## 2. Execution Log
```
docker run --rm -i --network=booking_monitor_default -e VUS=5000 -e DURATION=60s -v /Users/lileon/project/booking_monitor/scripts/k6_load.js:/script.js grafana/k6 run /script.js

         /\      Grafana   /‾‾/  
    /\  /  \     |\  __   /  /   
   /  \/    \    | |/ /  /   ‾‾\ 
  /          \   |   (  |  (‾)  |
 / __________ \  |_|\_\  \_____/ 


     execution: local
        script: /script.js
        output: -

     scenarios: (100.00%) 1 scenario, 5000 max VUs, 1m30s max duration (incl. graceful stop):
              * booking_stress: 5000 looping VUs for 1m0s (gracefulStop: 30s)


Init             [  88% ] 4389/5000 VUs initialized
booking_stress   [   0% ]

running (0m00.6s), 0978/5000 VUs, 436 complete and 0 interrupted iterations
booking_stress   [   1% ] 5000 VUs  0m00.5s/1m0s

running (0m01.6s), 2673/5000 VUs, 3246 complete and 0 interrupted iterations
booking_stress   [   3% ] 5000 VUs  0m01.5s/1m0s

running (0m02.6s), 3781/5000 VUs, 5032 complete and 0 interrupted iterations
booking_stress   [   4% ] 5000 VUs  0m02.5s/1m0s

running (0m03.6s), 4810/5000 VUs, 7983 complete and 0 interrupted iterations
booking_stress   [   6% ] 5000 VUs  0m03.5s/1m0s

running (0m04.6s), 5000/5000 VUs, 8686 complete and 0 interrupted iterations
booking_stress   [   8% ] 5000 VUs  0m04.5s/1m0s

running (0m05.6s), 5000/5000 VUs, 8851 complete and 0 interrupted iterations
booking_stress   [   9% ] 5000 VUs  0m05.5s/1m0s

running (0m07.0s), 5000/5000 VUs, 10312 complete and 0 interrupted iterations
booking_stress   [  12% ] 5000 VUs  0m06.9s/1m0s

running (0m07.6s), 5000/5000 VUs, 10678 complete and 0 interrupted iterations
booking_stress   [  13% ] 5000 VUs  0m07.5s/1m0s

running (0m08.6s), 5000/5000 VUs, 11100 complete and 0 interrupted iterations
booking_stress   [  14% ] 5000 VUs  0m08.5s/1m0s

running (0m09.6s), 5000/5000 VUs, 13398 complete and 0 interrupted iterations
booking_stress   [  16% ] 5000 VUs  0m09.5s/1m0s

running (0m10.6s), 5000/5000 VUs, 15502 complete and 0 interrupted iterations
booking_stress   [  18% ] 5000 VUs  0m10.5s/1m0s

running (0m11.7s), 5000/5000 VUs, 17563 complete and 0 interrupted iterations
booking_stress   [  19% ] 5000 VUs  0m11.6s/1m0s

running (0m12.6s), 5000/5000 VUs, 19004 complete and 0 interrupted iterations
booking_stress   [  21% ] 5000 VUs  0m12.5s/1m0s

running (0m13.6s), 5000/5000 VUs, 20778 complete and 0 interrupted iterations
booking_stress   [  23% ] 5000 VUs  0m13.5s/1m0s

running (0m14.6s), 5000/5000 VUs, 21907 complete and 0 interrupted iterations
booking_stress   [  24% ] 5000 VUs  0m14.5s/1m0s

running (0m15.6s), 5000/5000 VUs, 22853 complete and 0 interrupted iterations
booking_stress   [  26% ] 5000 VUs  0m15.5s/1m0s

running (0m16.6s), 5000/5000 VUs, 23323 complete and 0 interrupted iterations
booking_stress   [  28% ] 5000 VUs  0m16.5s/1m0s

running (0m17.6s), 5000/5000 VUs, 25652 complete and 0 interrupted iterations
booking_stress   [  29% ] 5000 VUs  0m17.5s/1m0s

running (0m18.6s), 5000/5000 VUs, 27323 complete and 0 interrupted iterations
booking_stress   [  31% ] 5000 VUs  0m18.5s/1m0s

running (0m19.6s), 5000/5000 VUs, 28861 complete and 0 interrupted iterations
booking_stress   [  33% ] 5000 VUs  0m19.5s/1m0s

running (0m20.6s), 5000/5000 VUs, 29496 complete and 0 interrupted iterations
booking_stress   [  34% ] 5000 VUs  0m20.5s/1m0s

running (0m21.6s), 5000/5000 VUs, 31280 complete and 0 interrupted iterations
booking_stress   [  36% ] 5000 VUs  0m21.5s/1m0s

running (0m22.6s), 5000/5000 VUs, 35918 complete and 0 interrupted iterations
booking_stress   [  38% ] 5000 VUs  0m22.5s/1m0s

running (0m23.6s), 5000/5000 VUs, 38819 complete and 0 interrupted iterations
booking_stress   [  39% ] 5000 VUs  0m23.5s/1m0s

running (0m24.7s), 5000/5000 VUs, 40939 complete and 0 interrupted iterations
booking_stress   [  41% ] 5000 VUs  0m24.6s/1m0s

running (0m25.6s), 5000/5000 VUs, 41701 complete and 0 interrupted iterations
booking_stress   [  43% ] 5000 VUs  0m25.5s/1m0s

running (0m26.6s), 5000/5000 VUs, 43377 complete and 0 interrupted iterations
booking_stress   [  44% ] 5000 VUs  0m26.5s/1m0s

running (0m27.6s), 5000/5000 VUs, 46462 complete and 0 interrupted iterations
booking_stress   [  46% ] 5000 VUs  0m27.5s/1m0s

running (0m28.6s), 5000/5000 VUs, 47772 complete and 0 interrupted iterations
booking_stress   [  48% ] 5000 VUs  0m28.5s/1m0s

running (0m29.6s), 5000/5000 VUs, 48254 complete and 0 interrupted iterations
booking_stress   [  49% ] 5000 VUs  0m29.5s/1m0s

running (0m30.6s), 5000/5000 VUs, 49982 complete and 0 interrupted iterations
booking_stress   [  51% ] 5000 VUs  0m30.5s/1m0s

running (0m31.6s), 5000/5000 VUs, 51335 complete and 0 interrupted iterations
booking_stress   [  53% ] 5000 VUs  0m31.5s/1m0s

running (0m32.6s), 5000/5000 VUs, 52020 complete and 0 interrupted iterations
booking_stress   [  54% ] 5000 VUs  0m32.5s/1m0s

running (0m33.6s), 5000/5000 VUs, 52950 complete and 0 interrupted iterations
booking_stress   [  56% ] 5000 VUs  0m33.5s/1m0s

running (0m34.6s), 5000/5000 VUs, 53342 complete and 0 interrupted iterations
booking_stress   [  58% ] 5000 VUs  0m34.5s/1m0s

running (0m35.6s), 5000/5000 VUs, 55222 complete and 0 interrupted iterations
booking_stress   [  59% ] 5000 VUs  0m35.5s/1m0s

running (0m36.6s), 5000/5000 VUs, 55694 complete and 0 interrupted iterations
booking_stress   [  61% ] 5000 VUs  0m36.5s/1m0s

running (0m37.6s), 5000/5000 VUs, 56485 complete and 0 interrupted iterations
booking_stress   [  63% ] 5000 VUs  0m37.5s/1m0s

running (0m38.6s), 5000/5000 VUs, 59475 complete and 0 interrupted iterations
booking_stress   [  64% ] 5000 VUs  0m38.5s/1m0s

running (0m39.6s), 5000/5000 VUs, 61372 complete and 0 interrupted iterations
booking_stress   [  66% ] 5000 VUs  0m39.5s/1m0s

running (0m40.6s), 5000/5000 VUs, 65106 complete and 0 interrupted iterations
booking_stress   [  68% ] 5000 VUs  0m40.5s/1m0s

running (0m41.6s), 5000/5000 VUs, 66122 complete and 0 interrupted iterations
booking_stress   [  69% ] 5000 VUs  0m41.5s/1m0s

running (0m42.6s), 5000/5000 VUs, 67991 complete and 0 interrupted iterations
booking_stress   [  71% ] 5000 VUs  0m42.5s/1m0s

running (0m43.6s), 5000/5000 VUs, 69379 complete and 0 interrupted iterations
booking_stress   [  73% ] 5000 VUs  0m43.5s/1m0s

running (0m44.6s), 5000/5000 VUs, 70508 complete and 0 interrupted iterations
booking_stress   [  74% ] 5000 VUs  0m44.5s/1m0s

running (0m45.6s), 5000/5000 VUs, 72532 complete and 0 interrupted iterations
booking_stress   [  76% ] 5000 VUs  0m45.5s/1m0s

running (0m46.6s), 5000/5000 VUs, 73775 complete and 0 interrupted iterations
booking_stress   [  78% ] 5000 VUs  0m46.5s/1m0s

running (0m47.6s), 5000/5000 VUs, 74452 complete and 0 interrupted iterations
booking_stress   [  79% ] 5000 VUs  0m47.5s/1m0s

running (0m48.6s), 5000/5000 VUs, 75082 complete and 0 interrupted iterations
booking_stress   [  81% ] 5000 VUs  0m48.5s/1m0s

running (0m49.6s), 5000/5000 VUs, 75775 complete and 0 interrupted iterations
booking_stress   [  83% ] 5000 VUs  0m49.5s/1m0s

running (0m50.6s), 5000/5000 VUs, 75890 complete and 0 interrupted iterations
booking_stress   [  84% ] 5000 VUs  0m50.5s/1m0s

running (0m51.6s), 5000/5000 VUs, 76268 complete and 0 interrupted iterations
booking_stress   [  86% ] 5000 VUs  0m51.5s/1m0s

running (0m52.7s), 5000/5000 VUs, 77820 complete and 0 interrupted iterations
booking_stress   [  88% ] 5000 VUs  0m52.6s/1m0s

running (0m53.6s), 5000/5000 VUs, 78696 complete and 0 interrupted iterations
booking_stress   [  89% ] 5000 VUs  0m53.5s/1m0s

running (0m54.6s), 5000/5000 VUs, 79693 complete and 0 interrupted iterations
booking_stress   [  91% ] 5000 VUs  0m54.5s/1m0s

running (0m55.7s), 5000/5000 VUs, 80485 complete and 0 interrupted iterations
booking_stress   [  93% ] 5000 VUs  0m55.6s/1m0s

running (0m56.6s), 5000/5000 VUs, 80907 complete and 0 interrupted iterations
booking_stress   [  94% ] 5000 VUs  0m56.5s/1m0s

running (0m57.6s), 5000/5000 VUs, 81149 complete and 0 interrupted iterations
booking_stress   [  96% ] 5000 VUs  0m57.6s/1m0s

running (0m58.6s), 5000/5000 VUs, 81658 complete and 0 interrupted iterations
booking_stress   [  98% ] 5000 VUs  0m58.5s/1m0s

running (0m59.6s), 5000/5000 VUs, 82906 complete and 0 interrupted iterations
booking_stress   [  99% ] 5000 VUs  0m59.5s/1m0s

running (1m00.7s), 3434/5000 VUs, 85197 complete and 0 interrupted iterations
booking_stress ↓ [ 100% ] 5000 VUs  1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✗ 'p(95)<200' p(95)=836.37ms


  █ TOTAL RESULTS 

    checks_total.......: 88631   1451.73319/s
    checks_succeeded...: 100.00% 88631 out of 88631
    checks_failed......: 0.00%   0 out of 88631

    ✓ setup event created
    ✓ status is 200 or 409

    CUSTOM
    business_errors................: 0.00%  0 out of 88630

    HTTP
    http_req_duration..............: avg=183.65ms min=347.79µs med=78.12ms  max=4.48s p(90)=524.71ms p(95)=836.37ms
      { expected_response:true }...: avg=455.24ms min=758.87µs med=266.82ms max=4.48s p(90)=928.24ms p(95)=1.26s   
    http_req_failed................: 81.26% 72027 out of 88631
    http_reqs......................: 88631  1451.73319/s

    EXECUTION
    iteration_duration.............: avg=2.16s    min=10.47ms  med=1.86s    max=7.23s p(90)=4.05s    p(95)=4.47s   
    iterations.....................: 88630  1451.716811/s
    vus............................: 3394   min=0              max=5000
    vus_max........................: 5000   min=4391           max=5000

    NETWORK
    data_received..................: 13 MB  218 kB/s
    data_sent......................: 15 MB  248 kB/s




running (1m01.1s), 0000/5000 VUs, 88630 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 5000 VUs  1m0s
time="2026-02-15T21:41:46Z" level=error msg="thresholds on metrics 'http_req_duration' have been crossed"
make[1]: *** [stress-k6] Error 99
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 13.39% | 50.25MiB / 3.827GiB |
| **booking_redis** | 42.62% | 22.52MiB / 3.827GiB |
| **booking_app** | 191.22% | 210.5MiB / 3.827GiB |

❌ **Result**: FAIL
