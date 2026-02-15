# Benchmark Report: 500 Concurrent Users
**Date**: Sun Feb 15 00:47:05 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | 67234b4 |
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


running (0m00.8s), 500/500 VUs, 155 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  0m00.7s/1m0s

running (0m01.8s), 500/500 VUs, 830 complete and 0 interrupted iterations
booking_stress   [   3% ] 500 VUs  0m01.7s/1m0s

running (0m02.8s), 500/500 VUs, 1587 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  0m02.7s/1m0s

running (0m03.8s), 500/500 VUs, 2503 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.7s/1m0s

running (0m04.8s), 500/500 VUs, 3144 complete and 0 interrupted iterations
booking_stress   [   8% ] 500 VUs  0m04.7s/1m0s

running (0m05.8s), 500/500 VUs, 3500 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.7s/1m0s

running (0m06.8s), 500/500 VUs, 3910 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  0m06.7s/1m0s

running (0m07.8s), 500/500 VUs, 4138 complete and 0 interrupted iterations
booking_stress   [  13% ] 500 VUs  0m07.7s/1m0s

running (0m08.8s), 500/500 VUs, 4727 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  0m08.7s/1m0s

running (0m09.8s), 500/500 VUs, 5656 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.7s/1m0s

running (0m10.8s), 500/500 VUs, 6524 complete and 0 interrupted iterations
booking_stress   [  18% ] 500 VUs  0m10.7s/1m0s

running (0m11.8s), 500/500 VUs, 7372 complete and 0 interrupted iterations
booking_stress   [  19% ] 500 VUs  0m11.7s/1m0s

running (0m12.8s), 500/500 VUs, 8223 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.7s/1m0s

running (0m13.8s), 500/500 VUs, 8912 complete and 0 interrupted iterations
booking_stress   [  23% ] 500 VUs  0m13.7s/1m0s

running (0m14.8s), 500/500 VUs, 9631 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.7s/1m0s

running (0m15.8s), 500/500 VUs, 10663 complete and 0 interrupted iterations
booking_stress   [  26% ] 500 VUs  0m15.7s/1m0s

running (0m16.8s), 500/500 VUs, 11182 complete and 0 interrupted iterations
booking_stress   [  28% ] 500 VUs  0m16.7s/1m0s

running (0m17.8s), 500/500 VUs, 12047 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.7s/1m0s

running (0m18.8s), 500/500 VUs, 12905 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.7s/1m0s

running (0m19.8s), 500/500 VUs, 13894 complete and 0 interrupted iterations
booking_stress   [  33% ] 500 VUs  0m19.7s/1m0s

running (0m20.8s), 500/500 VUs, 14802 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.7s/1m0s

running (0m21.8s), 500/500 VUs, 15601 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.7s/1m0s

running (0m22.8s), 500/500 VUs, 16428 complete and 0 interrupted iterations
booking_stress   [  38% ] 500 VUs  0m22.7s/1m0s

running (0m23.8s), 500/500 VUs, 19550 complete and 0 interrupted iterations
booking_stress   [  39% ] 500 VUs  0m23.7s/1m0s

running (0m24.8s), 500/500 VUs, 22670 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  0m24.7s/1m0s

running (0m25.8s), 500/500 VUs, 25766 complete and 0 interrupted iterations
booking_stress   [  43% ] 500 VUs  0m25.7s/1m0s

running (0m26.8s), 500/500 VUs, 28256 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.7s/1m0s

running (0m27.8s), 500/500 VUs, 31637 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.7s/1m0s

running (0m28.8s), 500/500 VUs, 34378 complete and 0 interrupted iterations
booking_stress   [  48% ] 500 VUs  0m28.7s/1m0s

running (0m29.8s), 500/500 VUs, 38045 complete and 0 interrupted iterations
booking_stress   [  49% ] 500 VUs  0m29.7s/1m0s

running (0m30.8s), 500/500 VUs, 41463 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  0m30.7s/1m0s

running (0m31.8s), 500/500 VUs, 44369 complete and 0 interrupted iterations
booking_stress   [  53% ] 500 VUs  0m31.7s/1m0s

running (0m32.8s), 500/500 VUs, 47906 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.7s/1m0s

running (0m33.8s), 500/500 VUs, 50575 complete and 0 interrupted iterations
booking_stress   [  56% ] 500 VUs  0m33.7s/1m0s

running (0m34.8s), 500/500 VUs, 53397 complete and 0 interrupted iterations
booking_stress   [  58% ] 500 VUs  0m34.7s/1m0s

running (0m35.8s), 500/500 VUs, 57026 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.7s/1m0s

running (0m36.8s), 500/500 VUs, 59561 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.7s/1m0s

running (0m37.8s), 500/500 VUs, 62945 complete and 0 interrupted iterations
booking_stress   [  63% ] 500 VUs  0m37.7s/1m0s

running (0m38.8s), 500/500 VUs, 66057 complete and 0 interrupted iterations
booking_stress   [  64% ] 500 VUs  0m38.7s/1m0s

running (0m39.8s), 500/500 VUs, 69390 complete and 0 interrupted iterations
booking_stress   [  66% ] 500 VUs  0m39.7s/1m0s

running (0m40.8s), 500/500 VUs, 72888 complete and 0 interrupted iterations
booking_stress   [  68% ] 500 VUs  0m40.7s/1m0s

running (0m41.8s), 500/500 VUs, 75019 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.7s/1m0s

running (0m42.8s), 500/500 VUs, 78451 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  0m42.7s/1m0s

running (0m43.8s), 500/500 VUs, 81584 complete and 0 interrupted iterations
booking_stress   [  73% ] 500 VUs  0m43.7s/1m0s

running (0m44.8s), 500/500 VUs, 84073 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  0m44.7s/1m0s

running (0m45.8s), 500/500 VUs, 87069 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.7s/1m0s

running (0m46.8s), 500/500 VUs, 88881 complete and 0 interrupted iterations
booking_stress   [  78% ] 500 VUs  0m46.7s/1m0s

running (0m47.8s), 500/500 VUs, 91541 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.7s/1m0s

running (0m48.8s), 500/500 VUs, 93614 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  0m48.7s/1m0s

running (0m49.8s), 500/500 VUs, 96979 complete and 0 interrupted iterations
booking_stress   [  83% ] 500 VUs  0m49.7s/1m0s

running (0m50.8s), 500/500 VUs, 99729 complete and 0 interrupted iterations
booking_stress   [  84% ] 500 VUs  0m50.7s/1m0s

running (0m51.8s), 500/500 VUs, 102202 complete and 0 interrupted iterations
booking_stress   [  86% ] 500 VUs  0m51.7s/1m0s

running (0m52.8s), 500/500 VUs, 104957 complete and 0 interrupted iterations
booking_stress   [  88% ] 500 VUs  0m52.7s/1m0s

running (0m53.8s), 500/500 VUs, 106310 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.7s/1m0s

running (0m54.8s), 500/500 VUs, 108456 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.7s/1m0s

running (0m55.9s), 500/500 VUs, 110804 complete and 0 interrupted iterations
booking_stress   [  93% ] 500 VUs  0m55.7s/1m0s

running (0m56.8s), 500/500 VUs, 112735 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.7s/1m0s

running (0m57.8s), 500/500 VUs, 115599 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.7s/1m0s

running (0m58.8s), 500/500 VUs, 118355 complete and 0 interrupted iterations
booking_stress   [  98% ] 500 VUs  0m58.7s/1m0s

running (0m59.8s), 500/500 VUs, 121033 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.7s/1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.00%

    http_req_duration
    ✗ 'p(95)<200' p(95)=788.82ms


  █ TOTAL RESULTS 

    checks_total.......: 122484  2031.843859/s
    checks_succeeded...: 100.00% 122484 out of 122484
    checks_failed......: 0.00%   0 out of 122484

    ✓ setup event created
    ✓ status is 200 or 409

    CUSTOM
    business_errors................: 0.00%  0 out of 122483

    HTTP
    http_req_duration..............: avg=243.7ms  min=803.54µs med=141.03ms max=8.33s p(90)=537.45ms p(95)=788.82ms
      { expected_response:true }...: avg=661.14ms min=6.85ms   med=464.3ms  max=8.33s p(90)=1.45s    p(95)=1.92s   
    http_req_failed................: 86.40% 105829 out of 122484
    http_reqs......................: 122484 2031.843859/s

    EXECUTION
    iteration_duration.............: avg=244.22ms min=872.7µs  med=141.14ms max=8.34s p(90)=538.54ms p(95)=791.99ms
    iterations.....................: 122483 2031.82727/s
    vus............................: 500    min=500              max=500
    vus_max........................: 500    min=500              max=500

    NETWORK
    data_received..................: 18 MB  304 kB/s
    data_sent......................: 21 MB  347 kB/s




running (1m00.3s), 000/500 VUs, 122483 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
time="2026-02-15T08:48:06Z" level=error msg="thresholds on metrics 'http_req_duration' have been crossed"
make[1]: *** [stress-k6] Error 99
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 212.18% | 103.6MiB / 3.827GiB |
| **booking_app** | 143.83% | 267.3MiB / 3.827GiB |

❌ **Result**: FAIL
