# Benchmark Report: 500 Concurrent Users
**Date**: Sun Feb 15 21:13:35 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | 65058a9 |
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


running (0m00.6s), 500/500 VUs, 227 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  0m00.3s/1m0s

running (0m01.6s), 500/500 VUs, 10113 complete and 0 interrupted iterations
booking_stress   [   2% ] 500 VUs  0m01.3s/1m0s

running (0m02.6s), 500/500 VUs, 14798 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  0m02.3s/1m0s

running (0m03.6s), 500/500 VUs, 20939 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.3s/1m0s

running (0m04.6s), 500/500 VUs, 23080 complete and 0 interrupted iterations
booking_stress   [   7% ] 500 VUs  0m04.3s/1m0s

running (0m05.6s), 500/500 VUs, 23438 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.3s/1m0s

running (0m06.6s), 500/500 VUs, 27704 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  0m06.3s/1m0s

running (0m07.6s), 500/500 VUs, 33394 complete and 0 interrupted iterations
booking_stress   [  12% ] 500 VUs  0m07.3s/1m0s

running (0m08.6s), 500/500 VUs, 41709 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  0m08.3s/1m0s

running (0m09.6s), 500/500 VUs, 55920 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.3s/1m0s

running (0m10.6s), 500/500 VUs, 72143 complete and 0 interrupted iterations
booking_stress   [  17% ] 500 VUs  0m10.3s/1m0s

running (0m11.6s), 500/500 VUs, 84970 complete and 0 interrupted iterations
booking_stress   [  19% ] 500 VUs  0m11.3s/1m0s

running (0m12.6s), 500/500 VUs, 91111 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.3s/1m0s

running (0m13.6s), 500/500 VUs, 101164 complete and 0 interrupted iterations
booking_stress   [  22% ] 500 VUs  0m13.3s/1m0s

running (0m14.6s), 500/500 VUs, 115482 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.4s/1m0s

running (0m15.6s), 500/500 VUs, 128721 complete and 0 interrupted iterations
booking_stress   [  26% ] 500 VUs  0m15.3s/1m0s

running (0m16.6s), 500/500 VUs, 143481 complete and 0 interrupted iterations
booking_stress   [  27% ] 500 VUs  0m16.3s/1m0s

running (0m17.6s), 500/500 VUs, 157619 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.3s/1m0s

running (0m18.6s), 500/500 VUs, 171776 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.3s/1m0s

running (0m19.6s), 500/500 VUs, 179309 complete and 0 interrupted iterations
booking_stress   [  32% ] 500 VUs  0m19.3s/1m0s

running (0m20.6s), 500/500 VUs, 190724 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.3s/1m0s

running (0m21.6s), 500/500 VUs, 204257 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.3s/1m0s

running (0m22.6s), 500/500 VUs, 217226 complete and 0 interrupted iterations
booking_stress   [  37% ] 500 VUs  0m22.3s/1m0s

running (0m23.6s), 500/500 VUs, 227890 complete and 0 interrupted iterations
booking_stress   [  39% ] 500 VUs  0m23.3s/1m0s

running (0m24.6s), 500/500 VUs, 248221 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  0m24.3s/1m0s

running (0m25.6s), 500/500 VUs, 264012 complete and 0 interrupted iterations
booking_stress   [  42% ] 500 VUs  0m25.3s/1m0s

running (0m26.6s), 500/500 VUs, 285092 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.3s/1m0s

running (0m27.6s), 500/500 VUs, 305538 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.3s/1m0s

running (0m28.6s), 500/500 VUs, 326225 complete and 0 interrupted iterations
booking_stress   [  47% ] 500 VUs  0m28.3s/1m0s

running (0m29.6s), 500/500 VUs, 348316 complete and 0 interrupted iterations
booking_stress   [  49% ] 500 VUs  0m29.3s/1m0s

running (0m30.6s), 500/500 VUs, 364087 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  0m30.3s/1m0s

running (0m31.6s), 500/500 VUs, 383102 complete and 0 interrupted iterations
booking_stress   [  52% ] 500 VUs  0m31.3s/1m0s

running (0m32.6s), 500/500 VUs, 408535 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.3s/1m0s

running (0m33.6s), 500/500 VUs, 433125 complete and 0 interrupted iterations
booking_stress   [  56% ] 500 VUs  0m33.3s/1m0s

running (0m34.6s), 500/500 VUs, 450974 complete and 0 interrupted iterations
booking_stress   [  57% ] 500 VUs  0m34.3s/1m0s

running (0m35.6s), 500/500 VUs, 468951 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.3s/1m0s

running (0m36.6s), 500/500 VUs, 489255 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.3s/1m0s

running (0m37.6s), 500/500 VUs, 515305 complete and 0 interrupted iterations
booking_stress   [  62% ] 500 VUs  0m37.3s/1m0s

running (0m38.6s), 500/500 VUs, 537192 complete and 0 interrupted iterations
booking_stress   [  64% ] 500 VUs  0m38.3s/1m0s

running (0m39.6s), 500/500 VUs, 553952 complete and 0 interrupted iterations
booking_stress   [  66% ] 500 VUs  0m39.4s/1m0s

running (0m40.6s), 500/500 VUs, 574680 complete and 0 interrupted iterations
booking_stress   [  67% ] 500 VUs  0m40.3s/1m0s

running (0m41.6s), 500/500 VUs, 584735 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.3s/1m0s

running (0m42.6s), 500/500 VUs, 613217 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  0m42.3s/1m0s

running (0m43.6s), 500/500 VUs, 638847 complete and 0 interrupted iterations
booking_stress   [  72% ] 500 VUs  0m43.3s/1m0s

running (0m44.6s), 500/500 VUs, 659901 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  0m44.3s/1m0s

running (0m45.6s), 500/500 VUs, 676644 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.3s/1m0s

running (0m46.6s), 500/500 VUs, 693492 complete and 0 interrupted iterations
booking_stress   [  77% ] 500 VUs  0m46.3s/1m0s

running (0m47.6s), 500/500 VUs, 715500 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.3s/1m0s

running (0m48.6s), 500/500 VUs, 743214 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  0m48.3s/1m0s

running (0m49.6s), 500/500 VUs, 768724 complete and 0 interrupted iterations
booking_stress   [  82% ] 500 VUs  0m49.3s/1m0s

running (0m50.6s), 500/500 VUs, 787472 complete and 0 interrupted iterations
booking_stress   [  84% ] 500 VUs  0m50.3s/1m0s

running (0m51.6s), 500/500 VUs, 808937 complete and 0 interrupted iterations
booking_stress   [  86% ] 500 VUs  0m51.3s/1m0s

running (0m52.6s), 500/500 VUs, 839591 complete and 0 interrupted iterations
booking_stress   [  87% ] 500 VUs  0m52.3s/1m0s

running (0m53.6s), 500/500 VUs, 857379 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.3s/1m0s

running (0m54.6s), 500/500 VUs, 877066 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.3s/1m0s

running (0m55.6s), 500/500 VUs, 899730 complete and 0 interrupted iterations
booking_stress   [  92% ] 500 VUs  0m55.3s/1m0s

running (0m56.6s), 500/500 VUs, 913499 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.3s/1m0s

running (0m57.6s), 500/500 VUs, 939011 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.4s/1m0s

running (0m58.6s), 500/500 VUs, 961463 complete and 0 interrupted iterations
booking_stress   [  97% ] 500 VUs  0m58.3s/1m0s

running (0m59.6s), 500/500 VUs, 988701 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.3s/1m0s

running (1m00.4s), 000/500 VUs, 1002686 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=59.07ms


  █ TOTAL RESULTS 

    checks_total.......: 3008059 49776.936926/s
    checks_succeeded...: 66.33%  1995373 out of 3008059
    checks_failed......: 33.66%  1012686 out of 3008059

    ✓ setup event created
    ✓ status is 200 or 409
    ✗ is sold out
      ↳  0% — ✓ 0 / ✗ 1002686
    ✗ is duplicate
      ↳  99% — ✓ 992686 / ✗ 10000

    CUSTOM
    business_errors................: 0.00%   0 out of 1002686

    HTTP
    http_req_duration..............: avg=19.17ms min=61.79µs  med=11.73ms max=1.13s    p(90)=40.29ms  p(95)=59.07ms 
      { expected_response:true }...: avg=52.76ms min=145.45µs med=32.17ms max=667.94ms p(90)=121.36ms p(95)=166.62ms
    http_req_failed................: 99.00%  992686 out of 1002687
    http_reqs......................: 1002687 16592.323341/s

    EXECUTION
    iteration_duration.............: avg=29.67ms min=91.58µs  med=18.66ms max=1.96s    p(90)=60.14ms  p(95)=85.29ms 
    iterations.....................: 1002686 16592.306793/s
    vus............................: 500     min=500               max=500
    vus_max........................: 500     min=500               max=500

    NETWORK
    data_received..................: 167 MB  2.8 MB/s
    data_sent......................: 171 MB  2.8 MB/s




running (1m00.4s), 000/500 VUs, 1002686 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 1.36% | 39.9MiB / 3.827GiB |
| **booking_redis** | 81.08% | 17.01MiB / 3.827GiB |
| **booking_app** | 309.33% | 88.68MiB / 3.827GiB |

✅ **Result**: PASS
