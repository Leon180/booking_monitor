# `docs/demo/` — Pattern A 終端機 walkthrough

> English version: [README.md](README.md)

針對運作中的 stack 跑一段 3 分鐘的 asciinema 錄影,涵蓋完整的 Pattern A 流程 — 也就是 D10-minimal 的 portfolio 產出物(對應 [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) D10)。

## 資料夾內容

- `walkthrough.cast` — **PENDING**:把 [`scripts/d10_demo_walkthrough.sh`](../../scripts/d10_demo_walkthrough.sh) 對著新 stack 跑出來的 asciinema 錄影。純 JSON;當文字 commit。基礎建設(script、Makefile target、env 配方)在 PR #102 出版;`.cast` 本身會在 follow-up commit 才上來,因為錄影需要 host-side `brew install asciinema` + 一次互動式 session。下方「錄製」段有完整步驟。

## 錄影內容

三個階段(總長 ~3 分鐘):

1. **Phase 1 — happy path**(~30 秒):`book` → `pay` → `confirm-succeeded` → 輪詢直到 `paid`。展示 §5 forward recovery:每一段都有自己的 retry 安全網,不需要補償。
2. **Phase 2 — payment failed**(~30 秒):`book` → `pay` → `confirm-FAILED` → 輪詢直到 `compensated`。展示 §4 backward recovery:D5 webhook 的 payment_failed 路徑觸發 saga 補償。
3. **Phase 3 — abandonment**(~60 秒,大部分是 20 秒的 reservation window + sweep tick + saga round-trip):`book` 後不呼叫 `/pay` → 輪詢直到 `compensated`。展示 D6 expiry sweeper 作為 §4 backward recovery 的取消觸發來源。

對應的部落格文章 [`docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md`](../blog/2026-05-saga-pure-forward-recovery.zh-TW.md)([EN](../blog/2026-05-saga-pure-forward-recovery.md))解釋了這些階段背後的架構脈絡。

## 播放(等 .cast 上來之後)

```bash
# 安裝 asciinema(host-side,只裝一次)
brew install asciinema   # macOS;或跨平台用 `pipx install asciinema`

# 播放錄影
asciinema play docs/demo/walkthrough.cast

# 或用 2× 速度播放
asciinema play -s 2 docs/demo/walkthrough.cast
```

`.cast` 是純 JSON — 任何文字編輯器都能打開做檢查。

## 錄製(或重錄)

當底下的 flow 有變動(新狀態、新端點、env 語意改變),需要重錄:

```bash
# 1. 啟動 demo stack(20s reservation + 5s sweep tick)。
#    D10 walkthrough script 仰賴這兩個時序設定,讓 abandonment phase
#    維持在 ~60 秒以內。
make demo-up

# 2. 確認 stack 健康(livez + readyz 都回 200)
curl -sS http://localhost/livez
curl -sS http://localhost/readyz

# 3. 開錄。Walkthrough script 自帶 PAUSE_MS 控制階段間的停頓,
#    讓錄影節奏自然。
asciinema rec docs/demo/walkthrough.cast \
    --overwrite \
    --command='scripts/d10_demo_walkthrough.sh'

# 4. 回放確認;若哪個 phase 拖太慢,可以用 asciinema-edit 手動裁切。
#    通常一次錄成就能用,因為 script 本身的節奏就調好了。
asciinema play docs/demo/walkthrough.cast
```

`--overwrite` 允許覆蓋既有檔案。沒有這個 flag,asciinema 會拒絕覆寫(避免誤刪)。

### 節奏旋鈕

Walkthrough script 認 `PAUSE_MS`(預設 `1500`)作為步驟間的自然停頓。要快速重錄(例如 CI 或快速驗證)可以設 `PAUSE_MS=0`。停頓主要是給人工觀看時用;CI 跑 walkthrough script 本身一律設 `PAUSE_MS=0`。

### API origin

Walkthrough 預設 `API_ORIGIN=http://localhost`(nginx 在 host 80 port,是 `docker-compose.yml` 上唯一對外公開的位址)。只有當你不用 Docker compose、改用 `make run-server` 直接跑 Go binary 時,才需要把 `API_ORIGIN` 改成 `http://localhost:8080` — Docker compose 裡的 `app` service 只把 pprof 暴露在 `:6060`、**不是** `:8080`,從 host 打 `:8080` 會連不上。

## Optional:上傳到 asciinema.org

要嵌入到外部 portfolio 網站(履歷、個人頁)時,可以上傳到公開服務:

```bash
asciinema upload docs/demo/walkthrough.cast
# 會回傳一個 https://asciinema.org/a/<id> 之類的 URL
```

然後把這個 URL 加到 README 的終端機 walkthrough 段落,讓外部讀者不用 clone repo 就能在瀏覽器內播放。本資料夾內的 `.cast` 是 source of truth,asciinema.org 上的拷貝只是方便。
