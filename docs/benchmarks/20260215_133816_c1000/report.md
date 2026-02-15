# Benchmark Report: 1000 Concurrent Users
**Date**: Sun Feb 15 13:38:16 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | f9ff381 |
| **Parameters** | VUS=1000, DURATION=60s |
| **Database** | PostgreSQL 15.15 on aarch64-unknown-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit |

## 2. Execution Log
```
docker run --rm -i --network=booking_monitor_default -e VUS=1000 -e DURATION=60s -v /Users/lileon/project/booking_monitor/scripts/k6_load.js:/script.js grafana/k6 run /script.js

         /\      Grafana   /‾‾/  
    /\  /  \     |\  __   /  /   
   /  \/    \    | |/ /  /   ‾‾\ 
  /          \   |   (  |  (‾)  |
 / __________ \  |_|\_\  \_____/ 


     execution: local
        script: /script.js
        output: -

     scenarios: (100.00%) 1 scenario, 1000 max VUs, 1m30s max duration (incl. graceful stop):
              * booking_stress: 1000 looping VUs for 1m0s (gracefulStop: 30s)


running (0m00.4s), 1000/1000 VUs, 868 complete and 0 interrupted iterations
booking_stress   [   0% ] 1000 VUs  0m00.3s/1m0s

running (0m01.4s), 1000/1000 VUs, 7860 complete and 0 interrupted iterations
booking_stress   [   2% ] 1000 VUs  0m01.3s/1m0s

running (0m02.4s), 1000/1000 VUs, 18521 complete and 0 interrupted iterations
booking_stress   [   4% ] 1000 VUs  0m02.3s/1m0s

running (0m03.4s), 1000/1000 VUs, 27930 complete and 0 interrupted iterations
booking_stress   [   5% ] 1000 VUs  0m03.3s/1m0s

running (0m04.4s), 1000/1000 VUs, 39136 complete and 0 interrupted iterations
booking_stress   [   7% ] 1000 VUs  0m04.3s/1m0s

running (0m05.4s), 1000/1000 VUs, 48073 complete and 0 interrupted iterations
booking_stress   [   9% ] 1000 VUs  0m05.5s/1m0s

running (0m06.4s), 1000/1000 VUs, 59187 complete and 0 interrupted iterations
booking_stress   [  10% ] 1000 VUs  0m06.3s/1m0s

running (0m07.4s), 1000/1000 VUs, 69980 complete and 0 interrupted iterations
booking_stress   [  12% ] 1000 VUs  0m07.3s/1m0s

running (0m08.4s), 1000/1000 VUs, 83197 complete and 0 interrupted iterations
booking_stress   [  14% ] 1000 VUs  0m08.3s/1m0s

running (0m09.4s), 1000/1000 VUs, 96675 complete and 0 interrupted iterations
booking_stress   [  15% ] 1000 VUs  0m09.3s/1m0s

running (0m10.4s), 1000/1000 VUs, 103815 complete and 0 interrupted iterations
booking_stress   [  17% ] 1000 VUs  0m10.3s/1m0s

running (0m11.4s), 1000/1000 VUs, 115120 complete and 0 interrupted iterations
booking_stress   [  19% ] 1000 VUs  0m11.3s/1m0s

running (0m12.4s), 1000/1000 VUs, 124938 complete and 0 interrupted iterations
booking_stress   [  20% ] 1000 VUs  0m12.3s/1m0s

running (0m13.4s), 1000/1000 VUs, 131486 complete and 0 interrupted iterations
booking_stress   [  22% ] 1000 VUs  0m13.3s/1m0s

running (0m14.4s), 1000/1000 VUs, 141611 complete and 0 interrupted iterations
booking_stress   [  24% ] 1000 VUs  0m14.3s/1m0s

running (0m15.4s), 1000/1000 VUs, 152610 complete and 0 interrupted iterations
booking_stress   [  25% ] 1000 VUs  0m15.3s/1m0s

running (0m16.4s), 1000/1000 VUs, 163231 complete and 0 interrupted iterations
booking_stress   [  27% ] 1000 VUs  0m16.3s/1m0s

running (0m17.4s), 1000/1000 VUs, 175045 complete and 0 interrupted iterations
booking_stress   [  29% ] 1000 VUs  0m17.3s/1m0s

running (0m18.4s), 1000/1000 VUs, 187693 complete and 0 interrupted iterations
booking_stress   [  30% ] 1000 VUs  0m18.3s/1m0s

running (0m19.4s), 1000/1000 VUs, 198863 complete and 0 interrupted iterations
booking_stress   [  32% ] 1000 VUs  0m19.3s/1m0s

running (0m20.5s), 1000/1000 VUs, 208254 complete and 0 interrupted iterations
booking_stress   [  34% ] 1000 VUs  0m20.4s/1m0s

running (0m21.4s), 1000/1000 VUs, 218533 complete and 0 interrupted iterations
booking_stress   [  35% ] 1000 VUs  0m21.3s/1m0s

running (0m22.4s), 1000/1000 VUs, 226722 complete and 0 interrupted iterations
booking_stress   [  37% ] 1000 VUs  0m22.3s/1m0s

running (0m23.4s), 1000/1000 VUs, 240388 complete and 0 interrupted iterations
booking_stress   [  39% ] 1000 VUs  0m23.3s/1m0s

running (0m24.4s), 1000/1000 VUs, 252342 complete and 0 interrupted iterations
booking_stress   [  40% ] 1000 VUs  0m24.3s/1m0s

running (0m25.5s), 1000/1000 VUs, 264870 complete and 0 interrupted iterations
booking_stress   [  42% ] 1000 VUs  0m25.4s/1m0s

running (0m26.5s), 1000/1000 VUs, 276638 complete and 0 interrupted iterations
booking_stress   [  44% ] 1000 VUs  0m26.3s/1m0s

running (0m27.4s), 1000/1000 VUs, 281095 complete and 0 interrupted iterations
booking_stress   [  45% ] 1000 VUs  0m27.3s/1m0s

running (0m28.4s), 1000/1000 VUs, 300222 complete and 0 interrupted iterations
booking_stress   [  47% ] 1000 VUs  0m28.3s/1m0s

running (0m29.4s), 1000/1000 VUs, 315385 complete and 0 interrupted iterations
booking_stress   [  49% ] 1000 VUs  0m29.3s/1m0s

running (0m30.4s), 1000/1000 VUs, 323542 complete and 0 interrupted iterations
booking_stress   [  50% ] 1000 VUs  0m30.3s/1m0s

running (0m31.4s), 1000/1000 VUs, 333185 complete and 0 interrupted iterations
booking_stress   [  52% ] 1000 VUs  0m31.3s/1m0s

running (0m32.4s), 1000/1000 VUs, 336714 complete and 0 interrupted iterations
booking_stress   [  54% ] 1000 VUs  0m32.3s/1m0s

running (0m33.4s), 1000/1000 VUs, 344728 complete and 0 interrupted iterations
booking_stress   [  55% ] 1000 VUs  0m33.3s/1m0s

running (0m34.4s), 1000/1000 VUs, 356760 complete and 0 interrupted iterations
booking_stress   [  57% ] 1000 VUs  0m34.3s/1m0s

running (0m35.4s), 1000/1000 VUs, 369702 complete and 0 interrupted iterations
booking_stress   [  59% ] 1000 VUs  0m35.3s/1m0s

running (0m36.4s), 1000/1000 VUs, 381985 complete and 0 interrupted iterations
booking_stress   [  60% ] 1000 VUs  0m36.3s/1m0s

running (0m37.4s), 1000/1000 VUs, 394574 complete and 0 interrupted iterations
booking_stress   [  62% ] 1000 VUs  0m37.3s/1m0s

running (0m38.4s), 1000/1000 VUs, 408644 complete and 0 interrupted iterations
booking_stress   [  64% ] 1000 VUs  0m38.3s/1m0s

running (0m39.4s), 1000/1000 VUs, 420348 complete and 0 interrupted iterations
booking_stress   [  65% ] 1000 VUs  0m39.3s/1m0s

running (0m40.4s), 1000/1000 VUs, 432706 complete and 0 interrupted iterations
booking_stress   [  67% ] 1000 VUs  0m40.3s/1m0s

running (0m41.4s), 1000/1000 VUs, 439669 complete and 0 interrupted iterations
booking_stress   [  69% ] 1000 VUs  0m41.3s/1m0s

running (0m42.5s), 1000/1000 VUs, 447054 complete and 0 interrupted iterations
booking_stress   [  71% ] 1000 VUs  0m42.3s/1m0s

running (0m43.4s), 1000/1000 VUs, 458658 complete and 0 interrupted iterations
booking_stress   [  72% ] 1000 VUs  0m43.3s/1m0s

running (0m44.4s), 1000/1000 VUs, 470393 complete and 0 interrupted iterations
booking_stress   [  74% ] 1000 VUs  0m44.3s/1m0s

running (0m45.4s), 1000/1000 VUs, 483859 complete and 0 interrupted iterations
booking_stress   [  75% ] 1000 VUs  0m45.3s/1m0s

running (0m46.4s), 1000/1000 VUs, 491355 complete and 0 interrupted iterations
booking_stress   [  77% ] 1000 VUs  0m46.3s/1m0s

running (0m47.4s), 1000/1000 VUs, 504576 complete and 0 interrupted iterations
booking_stress   [  79% ] 1000 VUs  0m47.3s/1m0s

running (0m48.4s), 1000/1000 VUs, 518863 complete and 0 interrupted iterations
booking_stress   [  80% ] 1000 VUs  0m48.3s/1m0s

running (0m49.4s), 1000/1000 VUs, 534485 complete and 0 interrupted iterations
booking_stress   [  82% ] 1000 VUs  0m49.3s/1m0s

running (0m50.4s), 1000/1000 VUs, 543686 complete and 0 interrupted iterations
booking_stress   [  84% ] 1000 VUs  0m50.3s/1m0s

running (0m51.4s), 1000/1000 VUs, 553578 complete and 0 interrupted iterations
booking_stress   [  85% ] 1000 VUs  0m51.3s/1m0s

running (0m52.4s), 1000/1000 VUs, 563262 complete and 0 interrupted iterations
booking_stress   [  87% ] 1000 VUs  0m52.3s/1m0s

running (0m53.4s), 1000/1000 VUs, 573808 complete and 0 interrupted iterations
booking_stress   [  89% ] 1000 VUs  0m53.3s/1m0s

running (0m54.4s), 1000/1000 VUs, 586193 complete and 0 interrupted iterations
booking_stress   [  90% ] 1000 VUs  0m54.3s/1m0s

running (0m55.4s), 1000/1000 VUs, 597406 complete and 0 interrupted iterations
booking_stress   [  92% ] 1000 VUs  0m55.3s/1m0s

running (0m56.4s), 1000/1000 VUs, 609319 complete and 0 interrupted iterations
booking_stress   [  94% ] 1000 VUs  0m56.3s/1m0s

running (0m57.4s), 1000/1000 VUs, 622894 complete and 0 interrupted iterations
booking_stress   [  95% ] 1000 VUs  0m57.3s/1m0s

running (0m58.4s), 1000/1000 VUs, 635991 complete and 0 interrupted iterations
booking_stress   [  97% ] 1000 VUs  0m58.3s/1m0s

running (0m59.4s), 1000/1000 VUs, 650195 complete and 0 interrupted iterations
booking_stress   [  99% ] 1000 VUs  0m59.3s/1m0s

running (1m00.2s), 0000/1000 VUs, 661309 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 1000 VUs  1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=175.06ms


  █ TOTAL RESULTS 

    checks_total.......: 661310  10990.215392/s
    checks_succeeded...: 100.00% 661310 out of 661310
    checks_failed......: 0.00%   0 out of 661310

    ✓ setup event created
    ✓ status is 200 or 409

    CUSTOM
    business_errors................: 0.00%  0 out of 661309

    HTTP
    http_req_duration..............: avg=78.83ms min=226.87µs med=64.93ms max=876.29ms p(90)=137.67ms p(95)=175.06ms
      { expected_response:true }...: avg=63.21ms min=235.87µs med=49.08ms max=335.38ms p(90)=129.94ms p(95)=160.24ms
    http_req_failed................: 97.47% 644597 out of 661310
    http_reqs......................: 661310 10990.215392/s

    EXECUTION
    iteration_duration.............: avg=89.31ms min=396.7µs  med=68.62ms max=918.58ms p(90)=156.55ms p(95)=210.68ms
    iterations.....................: 661309 10990.198773/s
    vus............................: 1000   min=1000             max=1000
    vus_max........................: 1000   min=1000             max=1000

    NETWORK
    data_received..................: 99 MB  1.6 MB/s
    data_sent......................: 113 MB 1.9 MB/s




running (1m00.2s), 0000/1000 VUs, 661309 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 1000 VUs  1m0s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 9.58% | 50.22MiB / 3.827GiB |
| **booking_redis** | 73.16% | 22.75MiB / 3.827GiB |
| **booking_app** | 304.74% | 117.7MiB / 3.827GiB |

✅ **Result**: PASS
