# Benchmark Report: 500 Concurrent Users
**Date**: Sun Feb 15 23:06:42 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | fefa372 |
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


running (0m00.8s), 500/500 VUs, 1564 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  0m00.6s/1m0s

running (0m01.8s), 500/500 VUs, 17958 complete and 0 interrupted iterations
booking_stress   [   3% ] 500 VUs  0m01.7s/1m0s

running (0m02.8s), 500/500 VUs, 39123 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  0m02.6s/1m0s

running (0m03.8s), 500/500 VUs, 64375 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.6s/1m0s

running (0m04.8s), 500/500 VUs, 82738 complete and 0 interrupted iterations
booking_stress   [   8% ] 500 VUs  0m04.6s/1m0s

running (0m05.8s), 500/500 VUs, 105890 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.6s/1m0s

running (0m06.8s), 500/500 VUs, 128839 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  0m06.6s/1m0s

running (0m07.8s), 500/500 VUs, 148724 complete and 0 interrupted iterations
booking_stress   [  13% ] 500 VUs  0m07.6s/1m0s

running (0m08.8s), 500/500 VUs, 161954 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  0m08.7s/1m0s

running (0m09.8s), 500/500 VUs, 174702 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.6s/1m0s

running (0m10.8s), 500/500 VUs, 188994 complete and 0 interrupted iterations
booking_stress   [  18% ] 500 VUs  0m10.6s/1m0s

running (0m11.8s), 500/500 VUs, 204559 complete and 0 interrupted iterations
booking_stress   [  19% ] 500 VUs  0m11.6s/1m0s

running (0m12.8s), 500/500 VUs, 217312 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.6s/1m0s

running (0m13.8s), 500/500 VUs, 230978 complete and 0 interrupted iterations
booking_stress   [  23% ] 500 VUs  0m13.6s/1m0s

running (0m14.8s), 500/500 VUs, 241313 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.6s/1m0s

running (0m15.8s), 500/500 VUs, 256161 complete and 0 interrupted iterations
booking_stress   [  26% ] 500 VUs  0m15.6s/1m0s

running (0m16.8s), 500/500 VUs, 268133 complete and 0 interrupted iterations
booking_stress   [  28% ] 500 VUs  0m16.6s/1m0s

running (0m17.8s), 500/500 VUs, 282385 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.6s/1m0s

running (0m18.8s), 500/500 VUs, 294298 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.6s/1m0s

running (0m19.8s), 500/500 VUs, 307153 complete and 0 interrupted iterations
booking_stress   [  33% ] 500 VUs  0m19.6s/1m0s

running (0m20.8s), 500/500 VUs, 321922 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.6s/1m0s

running (0m21.8s), 500/500 VUs, 335761 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.6s/1m0s

running (0m22.8s), 500/500 VUs, 348726 complete and 0 interrupted iterations
booking_stress   [  38% ] 500 VUs  0m22.6s/1m0s

running (0m23.8s), 500/500 VUs, 360506 complete and 0 interrupted iterations
booking_stress   [  39% ] 500 VUs  0m23.6s/1m0s

running (0m24.8s), 500/500 VUs, 369033 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  0m24.6s/1m0s

running (0m25.8s), 500/500 VUs, 384955 complete and 0 interrupted iterations
booking_stress   [  43% ] 500 VUs  0m25.6s/1m0s

running (0m26.8s), 500/500 VUs, 398543 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.6s/1m0s

running (0m27.8s), 500/500 VUs, 411260 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.6s/1m0s

running (0m28.8s), 500/500 VUs, 423242 complete and 0 interrupted iterations
booking_stress   [  48% ] 500 VUs  0m28.7s/1m0s

running (0m29.8s), 500/500 VUs, 435133 complete and 0 interrupted iterations
booking_stress   [  49% ] 500 VUs  0m29.7s/1m0s

running (0m30.8s), 500/500 VUs, 448470 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  0m30.6s/1m0s

running (0m31.8s), 500/500 VUs, 465479 complete and 0 interrupted iterations
booking_stress   [  53% ] 500 VUs  0m31.7s/1m0s

running (0m32.8s), 500/500 VUs, 480634 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.6s/1m0s

running (0m33.8s), 500/500 VUs, 495151 complete and 0 interrupted iterations
booking_stress   [  56% ] 500 VUs  0m33.6s/1m0s

running (0m34.8s), 500/500 VUs, 507467 complete and 0 interrupted iterations
booking_stress   [  58% ] 500 VUs  0m34.6s/1m0s

running (0m35.8s), 500/500 VUs, 520422 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.6s/1m0s

running (0m36.8s), 500/500 VUs, 533422 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.6s/1m0s

running (0m37.8s), 500/500 VUs, 547423 complete and 0 interrupted iterations
booking_stress   [  63% ] 500 VUs  0m37.6s/1m0s

running (0m38.8s), 500/500 VUs, 561617 complete and 0 interrupted iterations
booking_stress   [  65% ] 500 VUs  0m38.7s/1m0s

running (0m39.8s), 500/500 VUs, 574470 complete and 0 interrupted iterations
booking_stress   [  66% ] 500 VUs  0m39.6s/1m0s

running (0m40.8s), 500/500 VUs, 588908 complete and 0 interrupted iterations
booking_stress   [  68% ] 500 VUs  0m40.6s/1m0s

running (0m41.8s), 500/500 VUs, 605914 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.6s/1m0s

running (0m42.8s), 500/500 VUs, 621929 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  0m42.6s/1m0s

running (0m43.8s), 500/500 VUs, 635814 complete and 0 interrupted iterations
booking_stress   [  73% ] 500 VUs  0m43.7s/1m0s

running (0m44.8s), 500/500 VUs, 644628 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  0m44.6s/1m0s

running (0m45.8s), 500/500 VUs, 654807 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.6s/1m0s

running (0m46.8s), 500/500 VUs, 669308 complete and 0 interrupted iterations
booking_stress   [  78% ] 500 VUs  0m46.6s/1m0s

running (0m47.8s), 500/500 VUs, 675330 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.6s/1m0s

running (0m48.8s), 500/500 VUs, 688484 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  0m48.6s/1m0s

running (0m49.8s), 500/500 VUs, 699790 complete and 0 interrupted iterations
booking_stress   [  83% ] 500 VUs  0m49.6s/1m0s

running (0m50.8s), 500/500 VUs, 709538 complete and 0 interrupted iterations
booking_stress   [  84% ] 500 VUs  0m50.7s/1m0s

running (0m51.8s), 500/500 VUs, 724944 complete and 0 interrupted iterations
booking_stress   [  86% ] 500 VUs  0m51.6s/1m0s

running (0m52.8s), 500/500 VUs, 740667 complete and 0 interrupted iterations
booking_stress   [  88% ] 500 VUs  0m52.6s/1m0s

running (0m53.8s), 500/500 VUs, 756997 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.6s/1m0s

running (0m54.8s), 500/500 VUs, 769357 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.6s/1m0s

running (0m55.8s), 500/500 VUs, 780940 complete and 0 interrupted iterations
booking_stress   [  93% ] 500 VUs  0m55.6s/1m0s

running (0m56.8s), 500/500 VUs, 796090 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.6s/1m0s

running (0m57.8s), 500/500 VUs, 812321 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.6s/1m0s

running (0m58.8s), 500/500 VUs, 825923 complete and 0 interrupted iterations
booking_stress   [  98% ] 500 VUs  0m58.6s/1m0s

running (0m59.8s), 500/500 VUs, 836796 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.6s/1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=70.13ms


  █ TOTAL RESULTS 

    checks_total.......: 2521828 41930.117732/s
    checks_succeeded...: 66.27%  1671219 out of 2521828
    checks_failed......: 33.72%  850609 out of 2521828

    ✓ setup event created
    ✓ status is 200 or 409
    ✗ is sold out
      ↳  0% — ✓ 0 / ✗ 840609
    ✗ is duplicate
      ↳  98% — ✓ 830609 / ✗ 10000

    CUSTOM
    business_errors................: 0.00%  0 out of 840609

    HTTP
    http_req_duration..............: avg=22.76ms min=107.66µs med=15.08ms max=416.78ms p(90)=50.57ms p(95)=70.13ms
      { expected_response:true }...: avg=24.28ms min=131.87µs med=13.4ms  max=248.54ms p(90)=55.49ms p(95)=71.23ms
    http_req_failed................: 98.81% 830609 out of 840610
    http_reqs......................: 840610 13976.716995/s

    EXECUTION
    iteration_duration.............: avg=35.19ms min=155.79µs med=25.56ms max=612.46ms p(90)=73.6ms  p(95)=96.75ms
    iterations.....................: 840609 13976.700369/s
    vus............................: 500    min=500              max=500
    vus_max........................: 500    min=500              max=500

    NETWORK
    data_received..................: 140 MB 2.3 MB/s
    data_sent......................: 144 MB 2.4 MB/s




running (1m00.1s), 000/500 VUs, 840609 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 5.64% | 15.54MiB / 3.827GiB |
| **booking_redis** | 56.13% | 16.85MiB / 3.827GiB |
| **booking_app** | 243.51% | 68.15MiB / 3.827GiB |

✅ **Result**: PASS
