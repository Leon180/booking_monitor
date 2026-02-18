# Benchmark Report: 500 Concurrent Users
**Date**: Tue Feb 17 22:16:18 PST 2026

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


running (0m00.4s), 500/500 VUs, 51 complete and 0 interrupted iterations
booking_stress   [   0% ] 500 VUs  0m00.3s/1m0s

running (0m01.4s), 500/500 VUs, 4877 complete and 0 interrupted iterations
booking_stress   [   2% ] 500 VUs  0m01.3s/1m0s

running (0m02.4s), 500/500 VUs, 24977 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  0m02.3s/1m0s

running (0m03.4s), 500/500 VUs, 44053 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.3s/1m0s

running (0m04.4s), 500/500 VUs, 58908 complete and 0 interrupted iterations
booking_stress   [   7% ] 500 VUs  0m04.3s/1m0s

running (0m05.4s), 500/500 VUs, 67685 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.3s/1m0s

running (0m06.4s), 500/500 VUs, 76854 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  0m06.3s/1m0s

running (0m07.4s), 500/500 VUs, 81825 complete and 0 interrupted iterations
booking_stress   [  12% ] 500 VUs  0m07.3s/1m0s

running (0m08.8s), 500/500 VUs, 82726 complete and 0 interrupted iterations
booking_stress   [  15% ] 500 VUs  0m08.8s/1m0s

running (0m09.4s), 500/500 VUs, 84408 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.3s/1m0s

running (0m10.3s), 500/500 VUs, 90646 complete and 0 interrupted iterations
booking_stress   [  17% ] 500 VUs  0m10.3s/1m0s

running (0m11.3s), 500/500 VUs, 96278 complete and 0 interrupted iterations
booking_stress   [  19% ] 500 VUs  0m11.3s/1m0s

running (0m12.3s), 500/500 VUs, 98607 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.3s/1m0s

running (0m13.4s), 500/500 VUs, 101989 complete and 0 interrupted iterations
booking_stress   [  22% ] 500 VUs  0m13.3s/1m0s

running (0m14.4s), 500/500 VUs, 104483 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.3s/1m0s

running (0m15.4s), 500/500 VUs, 105269 complete and 0 interrupted iterations
booking_stress   [  26% ] 500 VUs  0m15.3s/1m0s

running (0m16.4s), 500/500 VUs, 108171 complete and 0 interrupted iterations
booking_stress   [  27% ] 500 VUs  0m16.4s/1m0s

running (0m17.4s), 500/500 VUs, 111906 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.4s/1m0s

running (0m18.3s), 500/500 VUs, 115950 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.3s/1m0s

running (0m19.3s), 500/500 VUs, 117768 complete and 0 interrupted iterations
booking_stress   [  32% ] 500 VUs  0m19.3s/1m0s

running (0m20.3s), 500/500 VUs, 120034 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.3s/1m0s

running (0m21.4s), 500/500 VUs, 126151 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.3s/1m0s

running (0m22.3s), 500/500 VUs, 139055 complete and 0 interrupted iterations
booking_stress   [  37% ] 500 VUs  0m22.3s/1m0s

running (0m23.3s), 500/500 VUs, 150890 complete and 0 interrupted iterations
booking_stress   [  39% ] 500 VUs  0m23.3s/1m0s

running (0m24.3s), 500/500 VUs, 163888 complete and 0 interrupted iterations
booking_stress   [  40% ] 500 VUs  0m24.3s/1m0s

running (0m25.3s), 500/500 VUs, 173892 complete and 0 interrupted iterations
booking_stress   [  42% ] 500 VUs  0m25.3s/1m0s

running (0m26.3s), 500/500 VUs, 184316 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.3s/1m0s

running (0m27.4s), 500/500 VUs, 191612 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.4s/1m0s

running (0m28.3s), 500/500 VUs, 201032 complete and 0 interrupted iterations
booking_stress   [  47% ] 500 VUs  0m28.3s/1m0s

running (0m29.3s), 500/500 VUs, 210828 complete and 0 interrupted iterations
booking_stress   [  49% ] 500 VUs  0m29.3s/1m0s

running (0m30.4s), 500/500 VUs, 222051 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  0m30.3s/1m0s

running (0m31.3s), 500/500 VUs, 232819 complete and 0 interrupted iterations
booking_stress   [  52% ] 500 VUs  0m31.3s/1m0s

running (0m32.3s), 500/500 VUs, 244364 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.3s/1m0s

running (0m33.3s), 500/500 VUs, 256246 complete and 0 interrupted iterations
booking_stress   [  55% ] 500 VUs  0m33.3s/1m0s

running (0m34.4s), 500/500 VUs, 266501 complete and 0 interrupted iterations
booking_stress   [  57% ] 500 VUs  0m34.4s/1m0s

running (0m35.3s), 500/500 VUs, 274649 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.3s/1m0s

running (0m36.4s), 500/500 VUs, 284035 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.3s/1m0s

running (0m37.4s), 500/500 VUs, 292786 complete and 0 interrupted iterations
booking_stress   [  62% ] 500 VUs  0m37.3s/1m0s

running (0m38.4s), 500/500 VUs, 301103 complete and 0 interrupted iterations
booking_stress   [  64% ] 500 VUs  0m38.3s/1m0s

running (0m39.4s), 500/500 VUs, 309707 complete and 0 interrupted iterations
booking_stress   [  65% ] 500 VUs  0m39.3s/1m0s

running (0m40.4s), 500/500 VUs, 320880 complete and 0 interrupted iterations
booking_stress   [  67% ] 500 VUs  0m40.3s/1m0s

running (0m41.4s), 500/500 VUs, 328568 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.3s/1m0s

running (0m42.4s), 500/500 VUs, 332375 complete and 0 interrupted iterations
booking_stress   [  70% ] 500 VUs  0m42.3s/1m0s

running (0m43.4s), 500/500 VUs, 345379 complete and 0 interrupted iterations
booking_stress   [  72% ] 500 VUs  0m43.3s/1m0s

running (0m44.4s), 500/500 VUs, 354602 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  0m44.3s/1m0s

running (0m45.4s), 500/500 VUs, 362977 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.3s/1m0s

running (0m46.4s), 500/500 VUs, 373094 complete and 0 interrupted iterations
booking_stress   [  77% ] 500 VUs  0m46.3s/1m0s

running (0m47.5s), 500/500 VUs, 383940 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.4s/1m0s

running (0m48.4s), 500/500 VUs, 387105 complete and 0 interrupted iterations
booking_stress   [  80% ] 500 VUs  0m48.3s/1m0s

running (0m49.4s), 500/500 VUs, 393113 complete and 0 interrupted iterations
booking_stress   [  82% ] 500 VUs  0m49.3s/1m0s

running (0m50.4s), 500/500 VUs, 399927 complete and 0 interrupted iterations
booking_stress   [  84% ] 500 VUs  0m50.3s/1m0s

running (0m51.4s), 500/500 VUs, 409436 complete and 0 interrupted iterations
booking_stress   [  85% ] 500 VUs  0m51.3s/1m0s

running (0m52.4s), 500/500 VUs, 416420 complete and 0 interrupted iterations
booking_stress   [  87% ] 500 VUs  0m52.3s/1m0s

running (0m53.4s), 500/500 VUs, 428692 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.3s/1m0s

running (0m54.4s), 500/500 VUs, 437836 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.3s/1m0s

running (0m55.5s), 500/500 VUs, 446209 complete and 0 interrupted iterations
booking_stress   [  92% ] 500 VUs  0m55.4s/1m0s

running (0m56.4s), 500/500 VUs, 453629 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.3s/1m0s

running (0m57.4s), 500/500 VUs, 462535 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.3s/1m0s

running (0m58.4s), 500/500 VUs, 467964 complete and 0 interrupted iterations
booking_stress   [  97% ] 500 VUs  0m58.3s/1m0s

running (0m59.4s), 500/500 VUs, 476902 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.3s/1m0s

running (1m00.1s), 000/500 VUs, 483044 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✓ 'p(95)<200' p(95)=138.03ms


  █ TOTAL RESULTS 

    checks_total.......: 1449133 24103.215683/s
    checks_succeeded...: 65.51%  949346 out of 1449133
    checks_failed......: 34.48%  499787 out of 1449133

    ✓ setup event created
    ✓ status is 200 or 409
    ✗ is sold out
      ↳  96% — ✓ 466301 / ✗ 16743
    ✗ is duplicate
      ↳  0% — ✓ 0 / ✗ 483044

    CUSTOM
    business_errors................: 0.00%  0 out of 483044

    HTTP
    http_req_duration..............: avg=43.22ms min=83.08µs  med=25.28ms max=1.47s    p(90)=96.5ms   p(95)=138.03ms
      { expected_response:true }...: avg=36.82ms min=112.75µs med=16.92ms max=594.93ms p(90)=71.04ms  p(95)=112.32ms
    http_req_failed................: 96.53% 466301 out of 483045
    http_reqs......................: 483045 8034.416316/s

    EXECUTION
    iteration_duration.............: avg=61.51ms min=155.66µs med=37.2ms  max=1.79s    p(90)=131.18ms p(95)=186.71ms
    iterations.....................: 483044 8034.399683/s
    vus............................: 500    min=500              max=500
    vus_max........................: 500    min=500              max=500

    NETWORK
    data_received..................: 92 MB  1.5 MB/s
    data_sent......................: 82 MB  1.4 MB/s




running (1m00.1s), 000/500 VUs, 483044 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 27.11% | 53.82MiB / 3.827GiB |
| **booking_redis** | 89.31% | 16.86MiB / 3.827GiB |
| **booking_app** | 319.06% | 73.61MiB / 3.827GiB |

✅ **Result**: PASS
