# Benchmark Report: 500 Concurrent Users
**Date**: Sun Feb 15 13:29:36 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | f9ff381 |
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


running (0m00.8s), 500/500 VUs, 1056 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  0m00.8s/1m0s

running (0m01.8s), 500/500 VUs, 5401 complete and 0 interrupted iterations
booking_stress   [   3% ] 500 VUs  0m01.7s/1m0s

running (0m02.8s), 500/500 VUs, 10009 complete and 0 interrupted iterations
booking_stress   [   5% ] 500 VUs  0m02.7s/1m0s

running (0m03.8s), 500/500 VUs, 15228 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.7s/1m0s

running (0m04.8s), 500/500 VUs, 20429 complete and 0 interrupted iterations
booking_stress   [   8% ] 500 VUs  0m04.7s/1m0s

running (0m05.8s), 500/500 VUs, 26210 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.7s/1m0s

running (0m06.9s), 500/500 VUs, 29944 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  0m06.8s/1m0s

running (0m07.1s), 500/500 VUs, 31303 complete and 0 interrupted iterations
booking_stress   [  12% ] 500 VUs  0m07.1s/1m0s

running (0m07.4s), 500/500 VUs, 32557 complete and 0 interrupted iterations
booking_stress   [  12% ] 500 VUs  0m07.3s/1m0s

running (0m07.8s), 500/500 VUs, 35046 complete and 0 interrupted iterations
booking_stress   [  13% ] 500 VUs  0m07.7s/1m0s

running (0m08.8s), 500/500 VUs, 42856 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  0m08.7s/1m0s

running (0m09.8s), 500/500 VUs, 46912 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.8s/1m0s

running (0m10.8s), 500/500 VUs, 48611 complete and 0 interrupted iterations
booking_stress   [  18% ] 500 VUs  0m10.7s/1m0s

running (0m11.8s), 500/500 VUs, 50335 complete and 0 interrupted iterations
booking_stress   [  20% ] 500 VUs  0m11.7s/1m0s

running (0m12.8s), 500/500 VUs, 54115 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.7s/1m0s

running (0m13.8s), 500/500 VUs, 64055 complete and 0 interrupted iterations
booking_stress   [  23% ] 500 VUs  0m13.7s/1m0s

running (0m14.8s), 500/500 VUs, 77115 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.7s/1m0s

running (0m15.8s), 500/500 VUs, 90293 complete and 0 interrupted iterations
booking_stress   [  26% ] 500 VUs  0m15.7s/1m0s

running (0m16.8s), 500/500 VUs, 103775 complete and 0 interrupted iterations
booking_stress   [  28% ] 500 VUs  0m16.7s/1m0s

running (0m17.8s), 500/500 VUs, 120157 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.7s/1m0s

running (0m18.8s), 500/500 VUs, 134198 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.7s/1m0s

running (0m19.8s), 500/500 VUs, 149042 complete and 0 interrupted iterations
booking_stress   [  33% ] 500 VUs  0m19.7s/1m0s

running (0m20.8s), 500/500 VUs, 161948 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.7s/1m0s

running (0m21.8s), 500/500 VUs, 173805 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.7s/1m0s

running (0m22.8s), 500/500 VUs, 188988 complete and 0 interrupted iterations
booking_stress   [  38% ] 500 VUs  0m22.7s/1m0s

running (0m23.8s), 500/500 VUs, 204369 complete and 0 interrupted iterations
booking_stress   [  39% ] 500 VUs  0m23.7s/1m0s

running (0m24.8s), 500/500 VUs, 220893 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  0m24.7s/1m0s

running (0m25.8s), 500/500 VUs, 235895 complete and 0 interrupted iterations
booking_stress   [  43% ] 500 VUs  0m25.7s/1m0s

running (0m26.8s), 500/500 VUs, 249915 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.7s/1m0s

running (0m27.8s), 500/500 VUs, 264720 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.8s/1m0s

running (0m28.8s), 500/500 VUs, 274111 complete and 0 interrupted iterations
booking_stress   [  48% ] 500 VUs  0m28.7s/1m0s

running (0m29.8s), 500/500 VUs, 282636 complete and 0 interrupted iterations
booking_stress   [  49% ] 500 VUs  0m29.7s/1m0s

running (0m30.8s), 500/500 VUs, 293084 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  0m30.7s/1m0s

running (0m31.8s), 500/500 VUs, 305659 complete and 0 interrupted iterations
booking_stress   [  53% ] 500 VUs  0m31.7s/1m0s

running (0m32.8s), 500/500 VUs, 318452 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.7s/1m0s

running (0m33.8s), 500/500 VUs, 328845 complete and 0 interrupted iterations
booking_stress   [  56% ] 500 VUs  0m33.7s/1m0s

running (0m34.8s), 500/500 VUs, 342481 complete and 0 interrupted iterations
booking_stress   [  58% ] 500 VUs  0m34.7s/1m0s

running (0m35.8s), 500/500 VUs, 356057 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.7s/1m0s

running (0m36.8s), 500/500 VUs, 369016 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.7s/1m0s

running (0m37.8s), 500/500 VUs, 384885 complete and 0 interrupted iterations
booking_stress   [  63% ] 500 VUs  0m37.7s/1m0s

running (0m38.8s), 500/500 VUs, 399897 complete and 0 interrupted iterations
booking_stress   [  64% ] 500 VUs  0m38.7s/1m0s

running (0m39.8s), 500/500 VUs, 412427 complete and 0 interrupted iterations
booking_stress   [  66% ] 500 VUs  0m39.7s/1m0s

running (0m40.8s), 500/500 VUs, 422791 complete and 0 interrupted iterations
booking_stress   [  68% ] 500 VUs  0m40.7s/1m0s

running (0m41.8s), 500/500 VUs, 434192 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.7s/1m0s

running (0m42.8s), 500/500 VUs, 448506 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  0m42.7s/1m0s

running (0m43.8s), 500/500 VUs, 464191 complete and 0 interrupted iterations
booking_stress   [  73% ] 500 VUs  0m43.7s/1m0s

running (0m44.8s), 500/500 VUs, 479980 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  0m44.7s/1m0s

running (0m45.8s), 500/500 VUs, 494255 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.7s/1m0s

running (0m46.8s), 500/500 VUs, 508528 complete and 0 interrupted iterations
booking_stress   [  78% ] 500 VUs  0m46.7s/1m0s

running (0m47.8s), 500/500 VUs, 526156 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.7s/1m0s

running (0m48.8s), 500/500 VUs, 539983 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  0m48.7s/1m0s

running (0m49.8s), 500/500 VUs, 548202 complete and 0 interrupted iterations
booking_stress   [  83% ] 500 VUs  0m49.7s/1m0s

running (0m50.8s), 500/500 VUs, 555561 complete and 0 interrupted iterations
booking_stress   [  85% ] 500 VUs  0m50.7s/1m0s

running (0m51.8s), 500/500 VUs, 562049 complete and 0 interrupted iterations
booking_stress   [  86% ] 500 VUs  0m51.7s/1m0s

running (0m52.8s), 500/500 VUs, 572398 complete and 0 interrupted iterations
booking_stress   [  88% ] 500 VUs  0m52.7s/1m0s

running (0m53.8s), 500/500 VUs, 583658 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.7s/1m0s

running (0m54.8s), 500/500 VUs, 598895 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.7s/1m0s

running (0m55.8s), 500/500 VUs, 612624 complete and 0 interrupted iterations
booking_stress   [  93% ] 500 VUs  0m55.7s/1m0s

running (0m56.8s), 500/500 VUs, 622640 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.7s/1m0s

running (0m57.8s), 500/500 VUs, 637984 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.7s/1m0s

running (0m58.8s), 500/500 VUs, 652614 complete and 0 interrupted iterations
booking_stress   [  98% ] 500 VUs  0m58.7s/1m0s

running (0m59.8s), 500/500 VUs, 662921 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.7s/1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=104.67ms


  █ TOTAL RESULTS 

    checks_total.......: 667727  11111.143167/s
    checks_succeeded...: 100.00% 667727 out of 667727
    checks_failed......: 0.00%   0 out of 667727

    ✓ setup event created
    ✓ status is 200 or 409

    CUSTOM
    business_errors................: 0.00%  0 out of 667726

    HTTP
    http_req_duration..............: avg=41.57ms min=214.33µs med=29.63ms max=1.17s    p(90)=76.78ms  p(95)=104.67ms
      { expected_response:true }...: avg=98.3ms  min=247.25µs med=66.1ms  max=676.01ms p(90)=235.41ms p(95)=320.78ms
    http_req_failed................: 97.50% 651089 out of 667727
    http_reqs......................: 667727 11111.143167/s

    EXECUTION
    iteration_duration.............: avg=44.75ms min=303.62µs med=31.01ms max=1.66s    p(90)=83.38ms  p(95)=113.75ms
    iterations.....................: 667726 11111.126526/s
    vus............................: 500    min=500              max=500
    vus_max........................: 500    min=500              max=500

    NETWORK
    data_received..................: 100 MB 1.7 MB/s
    data_sent......................: 114 MB 1.9 MB/s




running (1m00.1s), 000/500 VUs, 667726 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 64.14% | 50.23MiB / 3.827GiB |
| **booking_redis** | 111.83% | 22.7MiB / 3.827GiB |
| **booking_app** | 378.52% | 94.53MiB / 3.827GiB |

✅ **Result**: PASS
