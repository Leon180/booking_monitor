# Benchmark Report: 500 Concurrent Users
**Date**: Sun Feb 15 21:03:28 PST 2026

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


running (0m00.6s), 500/500 VUs, 1224 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  0m00.5s/1m0s

running (0m01.6s), 500/500 VUs, 15525 complete and 0 interrupted iterations
booking_stress   [   2% ] 500 VUs  0m01.5s/1m0s

running (0m02.6s), 500/500 VUs, 36658 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  0m02.5s/1m0s

running (0m03.6s), 500/500 VUs, 53330 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.5s/1m0s

running (0m04.6s), 500/500 VUs, 73321 complete and 0 interrupted iterations
booking_stress   [   7% ] 500 VUs  0m04.5s/1m0s

running (0m05.6s), 500/500 VUs, 99564 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.5s/1m0s

running (0m06.6s), 500/500 VUs, 123902 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  0m06.5s/1m0s

running (0m07.6s), 500/500 VUs, 145287 complete and 0 interrupted iterations
booking_stress   [  12% ] 500 VUs  0m07.5s/1m0s

running (0m08.6s), 500/500 VUs, 170520 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  0m08.5s/1m0s

running (0m09.6s), 500/500 VUs, 186115 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.5s/1m0s

running (0m10.6s), 500/500 VUs, 206761 complete and 0 interrupted iterations
booking_stress   [  17% ] 500 VUs  0m10.5s/1m0s

running (0m11.6s), 500/500 VUs, 230135 complete and 0 interrupted iterations
booking_stress   [  19% ] 500 VUs  0m11.5s/1m0s

running (0m12.6s), 500/500 VUs, 253138 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.5s/1m0s

running (0m13.6s), 500/500 VUs, 280621 complete and 0 interrupted iterations
booking_stress   [  22% ] 500 VUs  0m13.5s/1m0s

running (0m14.6s), 500/500 VUs, 308458 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.5s/1m0s

running (0m15.6s), 500/500 VUs, 330088 complete and 0 interrupted iterations
booking_stress   [  26% ] 500 VUs  0m15.5s/1m0s

running (0m16.6s), 500/500 VUs, 351969 complete and 0 interrupted iterations
booking_stress   [  27% ] 500 VUs  0m16.5s/1m0s

running (0m17.6s), 500/500 VUs, 380209 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.5s/1m0s

running (0m18.6s), 500/500 VUs, 407650 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.5s/1m0s

running (0m19.6s), 500/500 VUs, 430334 complete and 0 interrupted iterations
booking_stress   [  32% ] 500 VUs  0m19.5s/1m0s

running (0m20.6s), 500/500 VUs, 445198 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.5s/1m0s

running (0m21.6s), 500/500 VUs, 474244 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.5s/1m0s

running (0m22.6s), 500/500 VUs, 502023 complete and 0 interrupted iterations
booking_stress   [  37% ] 500 VUs  0m22.5s/1m0s

running (0m23.6s), 500/500 VUs, 521657 complete and 0 interrupted iterations
booking_stress   [  39% ] 500 VUs  0m23.5s/1m0s

running (0m24.6s), 500/500 VUs, 547130 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  0m24.5s/1m0s

running (0m25.6s), 500/500 VUs, 569876 complete and 0 interrupted iterations
booking_stress   [  42% ] 500 VUs  0m25.5s/1m0s

running (0m26.6s), 500/500 VUs, 597832 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.5s/1m0s

running (0m27.6s), 500/500 VUs, 627292 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.5s/1m0s

running (0m28.6s), 500/500 VUs, 656927 complete and 0 interrupted iterations
booking_stress   [  47% ] 500 VUs  0m28.5s/1m0s

running (0m29.6s), 500/500 VUs, 677055 complete and 0 interrupted iterations
booking_stress   [  49% ] 500 VUs  0m29.5s/1m0s

running (0m30.6s), 500/500 VUs, 692604 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  0m30.5s/1m0s

running (0m31.6s), 500/500 VUs, 716167 complete and 0 interrupted iterations
booking_stress   [  52% ] 500 VUs  0m31.5s/1m0s

running (0m32.6s), 500/500 VUs, 747431 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.5s/1m0s

running (0m33.6s), 500/500 VUs, 779653 complete and 0 interrupted iterations
booking_stress   [  56% ] 500 VUs  0m33.5s/1m0s

running (0m34.6s), 500/500 VUs, 815526 complete and 0 interrupted iterations
booking_stress   [  57% ] 500 VUs  0m34.5s/1m0s

running (0m35.6s), 500/500 VUs, 838230 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.5s/1m0s

running (0m36.6s), 500/500 VUs, 855733 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.5s/1m0s

running (0m37.6s), 500/500 VUs, 873001 complete and 0 interrupted iterations
booking_stress   [  62% ] 500 VUs  0m37.5s/1m0s

running (0m38.6s), 500/500 VUs, 902836 complete and 0 interrupted iterations
booking_stress   [  64% ] 500 VUs  0m38.5s/1m0s

running (0m39.6s), 500/500 VUs, 922082 complete and 0 interrupted iterations
booking_stress   [  66% ] 500 VUs  0m39.5s/1m0s

running (0m40.6s), 500/500 VUs, 933720 complete and 0 interrupted iterations
booking_stress   [  67% ] 500 VUs  0m40.5s/1m0s

running (0m41.6s), 500/500 VUs, 961962 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.5s/1m0s

running (0m42.6s), 500/500 VUs, 989624 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  0m42.5s/1m0s

running (0m43.6s), 500/500 VUs, 1022035 complete and 0 interrupted iterations
booking_stress   [  72% ] 500 VUs  0m43.5s/1m0s

running (0m44.6s), 500/500 VUs, 1048840 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  0m44.5s/1m0s

running (0m45.9s), 500/500 VUs, 1062765 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.8s/1m0s

running (0m46.6s), 500/500 VUs, 1067694 complete and 0 interrupted iterations
booking_stress   [  77% ] 500 VUs  0m46.5s/1m0s

running (0m47.6s), 500/500 VUs, 1069499 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.5s/1m0s

running (0m48.6s), 500/500 VUs, 1078487 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  0m48.5s/1m0s

running (0m49.6s), 500/500 VUs, 1093553 complete and 0 interrupted iterations
booking_stress   [  82% ] 500 VUs  0m49.5s/1m0s

running (0m50.6s), 500/500 VUs, 1113488 complete and 0 interrupted iterations
booking_stress   [  84% ] 500 VUs  0m50.5s/1m0s

running (0m51.6s), 500/500 VUs, 1141148 complete and 0 interrupted iterations
booking_stress   [  86% ] 500 VUs  0m51.5s/1m0s

running (0m52.6s), 500/500 VUs, 1166532 complete and 0 interrupted iterations
booking_stress   [  87% ] 500 VUs  0m52.5s/1m0s

running (0m53.6s), 500/500 VUs, 1190970 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.5s/1m0s

running (0m54.6s), 500/500 VUs, 1215938 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.5s/1m0s

running (0m55.6s), 500/500 VUs, 1232008 complete and 0 interrupted iterations
booking_stress   [  92% ] 500 VUs  0m55.5s/1m0s

running (0m56.6s), 500/500 VUs, 1254260 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.5s/1m0s

running (0m57.6s), 500/500 VUs, 1272542 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.5s/1m0s

running (0m58.6s), 500/500 VUs, 1290774 complete and 0 interrupted iterations
booking_stress   [  97% ] 500 VUs  0m58.5s/1m0s

running (0m59.6s), 500/500 VUs, 1304026 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.5s/1m0s

running (1m00.2s), 000/500 VUs, 1311101 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=42ms


  █ TOTAL RESULTS 

    checks_total.......: 3933304 65379.491449/s
    checks_succeeded...: 66.41%  2612203 out of 3933304
    checks_failed......: 33.58%  1321101 out of 3933304

    ✓ setup event created
    ✓ status is 200 or 409
    ✗ is sold out
      ↳  0% — ✓ 0 / ✗ 1311101
    ✗ is duplicate
      ↳  99% — ✓ 1301101 / ✗ 10000

    CUSTOM
    business_errors................: 0.00%   0 out of 1311101

    HTTP
    http_req_duration..............: avg=14.6ms  min=53.45µs  med=9.48ms  max=795.98ms p(90)=29.78ms p(95)=42ms   
      { expected_response:true }...: avg=33.56ms min=119.91µs med=16.47ms max=345.04ms p(90)=75.74ms p(95)=120.6ms
    http_req_failed................: 99.23%  1301101 out of 1311102
    http_reqs......................: 1311102 21793.174898/s

    EXECUTION
    iteration_duration.............: avg=22.65ms min=86.37µs  med=15.32ms max=1.37s    p(90)=45.04ms p(95)=60.52ms
    iterations.....................: 1311101 21793.158276/s
    vus............................: 500     min=500                max=500
    vus_max........................: 500     min=500                max=500

    NETWORK
    data_received..................: 219 MB  3.6 MB/s
    data_sent......................: 224 MB  3.7 MB/s




running (1m00.2s), 000/500 VUs, 1311101 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 8.96% | 35.12MiB / 3.827GiB |
| **booking_redis** | 59.69% | 17.07MiB / 3.827GiB |
| **booking_app** | 253.35% | 79.92MiB / 3.827GiB |

✅ **Result**: PASS
