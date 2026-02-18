# Benchmark Report: 500 Concurrent Users
**Date**: Wed Feb 18 10:31:32 PST 2026

## 1. Test Environment
| Component | Specification |
| :--- | :--- |
| **Host OS** | Darwin 25.2.0 |
| **Go Version** | go version go1.24.0 darwin/arm64 |
| **Commit** | df38baa |
| **Parameters** | VUS=500, DURATION=60s |
| **Database** | PostgreSQL 15.15 on aarch64-unknown-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit |

## 2. Execution Log
```
docker run --rm -i --network=booking_monitor_default -e VUS=500 -e DURATION=60s -v ./scripts/k6_load.js:/script.js grafana/k6 run /script.js

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


running (0m00.7s), 500/500 VUs, 1251 complete and 0 interrupted iterations
booking_stress   [   1% ] 500 VUs  0m00.6s/1m0s

running (0m01.7s), 500/500 VUs, 6057 complete and 0 interrupted iterations
booking_stress   [   3% ] 500 VUs  0m01.6s/1m0s

running (0m02.8s), 500/500 VUs, 7958 complete and 0 interrupted iterations
booking_stress   [   4% ] 500 VUs  0m02.7s/1m0s

running (0m03.7s), 500/500 VUs, 12039 complete and 0 interrupted iterations
booking_stress   [   6% ] 500 VUs  0m03.6s/1m0s

running (0m04.7s), 500/500 VUs, 23053 complete and 0 interrupted iterations
booking_stress   [   8% ] 500 VUs  0m04.6s/1m0s

running (0m05.7s), 500/500 VUs, 36592 complete and 0 interrupted iterations
booking_stress   [   9% ] 500 VUs  0m05.6s/1m0s

running (0m06.7s), 500/500 VUs, 57466 complete and 0 interrupted iterations
booking_stress   [  11% ] 500 VUs  0m06.6s/1m0s

running (0m07.7s), 500/500 VUs, 69258 complete and 0 interrupted iterations
booking_stress   [  13% ] 500 VUs  0m07.6s/1m0s

running (0m08.7s), 500/500 VUs, 72586 complete and 0 interrupted iterations
booking_stress   [  14% ] 500 VUs  0m08.6s/1m0s

running (0m09.7s), 500/500 VUs, 85838 complete and 0 interrupted iterations
booking_stress   [  16% ] 500 VUs  0m09.6s/1m0s

running (0m10.7s), 500/500 VUs, 102824 complete and 0 interrupted iterations
booking_stress   [  18% ] 500 VUs  0m10.6s/1m0s

running (0m11.7s), 500/500 VUs, 122340 complete and 0 interrupted iterations
booking_stress   [  19% ] 500 VUs  0m11.6s/1m0s

running (0m12.7s), 500/500 VUs, 146818 complete and 0 interrupted iterations
booking_stress   [  21% ] 500 VUs  0m12.6s/1m0s

running (0m13.7s), 500/500 VUs, 172699 complete and 0 interrupted iterations
booking_stress   [  23% ] 500 VUs  0m13.6s/1m0s

running (0m14.7s), 500/500 VUs, 200808 complete and 0 interrupted iterations
booking_stress   [  24% ] 500 VUs  0m14.6s/1m0s

running (0m15.7s), 500/500 VUs, 225124 complete and 0 interrupted iterations
booking_stress   [  26% ] 500 VUs  0m15.6s/1m0s

running (0m16.7s), 500/500 VUs, 247542 complete and 0 interrupted iterations
booking_stress   [  28% ] 500 VUs  0m16.6s/1m0s

running (0m17.7s), 500/500 VUs, 278673 complete and 0 interrupted iterations
booking_stress   [  29% ] 500 VUs  0m17.6s/1m0s

running (0m18.7s), 500/500 VUs, 303932 complete and 0 interrupted iterations
booking_stress   [  31% ] 500 VUs  0m18.6s/1m0s

running (0m19.7s), 500/500 VUs, 322008 complete and 0 interrupted iterations
booking_stress   [  33% ] 500 VUs  0m19.6s/1m0s

running (0m20.7s), 500/500 VUs, 346520 complete and 0 interrupted iterations
booking_stress   [  34% ] 500 VUs  0m20.6s/1m0s

running (0m21.7s), 500/500 VUs, 365367 complete and 0 interrupted iterations
booking_stress   [  36% ] 500 VUs  0m21.6s/1m0s

running (0m22.7s), 500/500 VUs, 387205 complete and 0 interrupted iterations
booking_stress   [  38% ] 500 VUs  0m22.6s/1m0s

running (0m23.7s), 500/500 VUs, 410554 complete and 0 interrupted iterations
booking_stress   [  39% ] 500 VUs  0m23.6s/1m0s

running (0m24.7s), 500/500 VUs, 433737 complete and 0 interrupted iterations
booking_stress   [  41% ] 500 VUs  0m24.6s/1m0s

running (0m25.7s), 500/500 VUs, 438600 complete and 0 interrupted iterations
booking_stress   [  43% ] 500 VUs  0m25.6s/1m0s

running (0m26.7s), 500/500 VUs, 464495 complete and 0 interrupted iterations
booking_stress   [  44% ] 500 VUs  0m26.6s/1m0s

running (0m27.7s), 500/500 VUs, 492840 complete and 0 interrupted iterations
booking_stress   [  46% ] 500 VUs  0m27.6s/1m0s

running (0m28.7s), 500/500 VUs, 511870 complete and 0 interrupted iterations
booking_stress   [  48% ] 500 VUs  0m28.6s/1m0s

running (0m29.6s), 500/500 VUs, 532969 complete and 0 interrupted iterations
booking_stress   [  49% ] 500 VUs  0m29.6s/1m0s

running (0m30.6s), 500/500 VUs, 552955 complete and 0 interrupted iterations
booking_stress   [  51% ] 500 VUs  0m30.6s/1m0s

running (0m31.7s), 500/500 VUs, 580896 complete and 0 interrupted iterations
booking_stress   [  53% ] 500 VUs  0m31.6s/1m0s

running (0m32.6s), 500/500 VUs, 606596 complete and 0 interrupted iterations
booking_stress   [  54% ] 500 VUs  0m32.6s/1m0s

running (0m33.6s), 500/500 VUs, 628117 complete and 0 interrupted iterations
booking_stress   [  56% ] 500 VUs  0m33.6s/1m0s

running (0m34.6s), 500/500 VUs, 655299 complete and 0 interrupted iterations
booking_stress   [  58% ] 500 VUs  0m34.6s/1m0s

running (0m35.6s), 500/500 VUs, 678292 complete and 0 interrupted iterations
booking_stress   [  59% ] 500 VUs  0m35.6s/1m0s

running (0m36.6s), 500/500 VUs, 696566 complete and 0 interrupted iterations
booking_stress   [  61% ] 500 VUs  0m36.6s/1m0s

running (0m37.6s), 500/500 VUs, 716342 complete and 0 interrupted iterations
booking_stress   [  63% ] 500 VUs  0m37.6s/1m0s

running (0m38.6s), 500/500 VUs, 747008 complete and 0 interrupted iterations
booking_stress   [  64% ] 500 VUs  0m38.6s/1m0s

running (0m39.6s), 500/500 VUs, 775285 complete and 0 interrupted iterations
booking_stress   [  66% ] 500 VUs  0m39.6s/1m0s

running (0m40.6s), 500/500 VUs, 804324 complete and 0 interrupted iterations
booking_stress   [  68% ] 500 VUs  0m40.6s/1m0s

running (0m41.6s), 500/500 VUs, 826428 complete and 0 interrupted iterations
booking_stress   [  69% ] 500 VUs  0m41.6s/1m0s

running (0m42.6s), 500/500 VUs, 851745 complete and 0 interrupted iterations
booking_stress   [  71% ] 500 VUs  0m42.6s/1m0s

running (0m43.6s), 500/500 VUs, 870358 complete and 0 interrupted iterations
booking_stress   [  73% ] 500 VUs  0m43.6s/1m0s

running (0m44.6s), 500/500 VUs, 893298 complete and 0 interrupted iterations
booking_stress   [  74% ] 500 VUs  0m44.6s/1m0s

running (0m45.7s), 500/500 VUs, 919159 complete and 0 interrupted iterations
booking_stress   [  76% ] 500 VUs  0m45.6s/1m0s

running (0m46.6s), 500/500 VUs, 940713 complete and 0 interrupted iterations
booking_stress   [  78% ] 500 VUs  0m46.6s/1m0s

running (0m47.6s), 500/500 VUs, 961815 complete and 0 interrupted iterations
booking_stress   [  79% ] 500 VUs  0m47.6s/1m0s

running (0m48.6s), 500/500 VUs, 980376 complete and 0 interrupted iterations
booking_stress   [  81% ] 500 VUs  0m48.6s/1m0s

running (0m49.6s), 500/500 VUs, 1003896 complete and 0 interrupted iterations
booking_stress   [  83% ] 500 VUs  0m49.6s/1m0s

running (0m50.7s), 500/500 VUs, 1025878 complete and 0 interrupted iterations
booking_stress   [  84% ] 500 VUs  0m50.6s/1m0s

running (0m51.6s), 500/500 VUs, 1045000 complete and 0 interrupted iterations
booking_stress   [  86% ] 500 VUs  0m51.6s/1m0s

running (0m52.6s), 500/500 VUs, 1052946 complete and 0 interrupted iterations
booking_stress   [  88% ] 500 VUs  0m52.6s/1m0s

running (0m53.7s), 500/500 VUs, 1057810 complete and 0 interrupted iterations
booking_stress   [  89% ] 500 VUs  0m53.6s/1m0s

running (0m54.6s), 500/500 VUs, 1072727 complete and 0 interrupted iterations
booking_stress   [  91% ] 500 VUs  0m54.6s/1m0s

running (0m55.6s), 500/500 VUs, 1096656 complete and 0 interrupted iterations
booking_stress   [  93% ] 500 VUs  0m55.6s/1m0s

running (0m56.6s), 500/500 VUs, 1122399 complete and 0 interrupted iterations
booking_stress   [  94% ] 500 VUs  0m56.6s/1m0s

running (0m57.6s), 500/500 VUs, 1154881 complete and 0 interrupted iterations
booking_stress   [  96% ] 500 VUs  0m57.6s/1m0s

running (0m58.6s), 500/500 VUs, 1182349 complete and 0 interrupted iterations
booking_stress   [  98% ] 500 VUs  0m58.6s/1m0s

running (0m59.7s), 500/500 VUs, 1206512 complete and 0 interrupted iterations
booking_stress   [  99% ] 500 VUs  0m59.6s/1m0s

running (1m00.1s), 000/500 VUs, 1213894 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s


  █ THRESHOLDS 

    business_errors
    ✓ 'rate<0.01' rate=0.01%

    http_req_duration
    ✓ 'p(95)<200' p(95)=46.17ms


  █ TOTAL RESULTS 

    checks_total.......: 3641683 60591.667912/s
    checks_succeeded...: 66.20%  2410802 out of 3641683
    checks_failed......: 33.79%  1230881 out of 3641683

    ✓ setup event created
    ✗ status is 200 or 409
      ↳  99% — ✓ 1213694 / ✗ 200
    ✗ is sold out
      ↳  98% — ✓ 1197107 / ✗ 16787
    ✗ is duplicate
      ↳  0% — ✓ 0 / ✗ 1213894

    CUSTOM
    business_errors................: 0.01%   200 out of 1213894

    HTTP
    http_req_duration..............: avg=16.06ms min=9.99µs   med=9.16ms  max=1.33s p(90)=31.58ms  p(95)=46.17ms 
      { expected_response:true }...: avg=86.72ms min=124.75µs med=43.34ms max=1.27s p(90)=167.98ms p(95)=300.85ms
    http_req_failed................: 98.63%  1197307 out of 1213895
    http_reqs......................: 1213895 20197.23373/s

    EXECUTION
    iteration_duration.............: avg=24.41ms min=98.41µs  med=15.34ms max=1.38s p(90)=46.42ms  p(95)=66.26ms 
    iterations.....................: 1213894 20197.217091/s
    vus............................: 500     min=500                max=500
    vus_max........................: 500     min=500                max=500

    NETWORK
    data_received..................: 231 MB  3.8 MB/s
    data_sent......................: 206 MB  3.4 MB/s




running (1m00.1s), 000/500 VUs, 1213894 complete and 0 interrupted iterations
booking_stress ✓ [ 100% ] 500 VUs  1m0s
```

## 3. Resource Usage (Peak)
| Container | Peak CPU | Peak Mem |
| :--- | :--- | :--- |
| **booking_db** | 22.72% | 87.46MiB / 3.827GiB |
| **booking_redis** | 75.87% | 24.66MiB / 3.827GiB |
| **booking_app** | 257.76% | 75.03MiB / 3.827GiB |

✅ **Result**: PASS
