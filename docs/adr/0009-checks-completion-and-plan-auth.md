# ADR 0009 — Checks, completion, attempts ve provider credentials

Tarih: 8 Ekim 2026. Sürüm: 0.8.0-dev. PRD authority ve A–D release gates değişmez.

## Check receipts ve completion

Operator CheckRuntime pinned profile’ı, CheckPlan argv/requirements/selection/explicit closure’ı task başlamadan seçer. Model yalnız check ID çağırır. Native intent journal’a yazıldıktan sonra fresh spec/candidate/generation/policy kontrolü ve offline broker dispatch yapılır. Host fallback yoktur.

Readonly/nonroot/no-network/private namespace ve exact source/image/argv/quiescence broker kontrolleridir. Exit/timeout/cancel/OOM/output-limit yalnız computation outcome’dur. Discovery UNTRUSTED_UNRESOLVED, verification UNKNOWN kalır. CAS receipt exact stdout/stderr ve candidate/spec/epoch/origin/profile/invocation bağını korur; historical candidate protected closure da revalidate edilir. Kanıtlı preflight denial DispatchFailure.EffectPossible=false olabilir. Creation denenmiş ve cleanup kanıtı yoksa UNKNOWN korunur.

Final, pending plan ve configured check computation’larını çözmeden ilerlemez. Bounded repair aynı spec/goal/work budget içindedir; semantic review değildir. ANALYSIS mutation admission öncesi reddedilir; CODE/no-change ayrı kind taşır. Artifact MODEL_AUTHORED_UNREVIEWED’dir.

## Yeni attempt

Terminal task yeniden açılmaz. Yeni task ID + exact parent terminal seq + request digest, raw intent/criteria source span’larını korur; live source yeniden capture edilir. Eski plan/check/onay/quality/context taşınmaz. Yeni task work sayacı store toplam charge’ını sıfırlamaz. UNKNOWN parent effect engellenir. Created/Scoping sonrası kesinti aynı ID/payload ile owner restart’ında uzlaştırılır.

## Abonelik auth

Yerel/açık kaynak SIWC sabit resmî authorize/token/JWKS/models/Responses/revocation origins kullanır. Loopback listener tarayıcıdan önce açılır; fresh state/nonce/PKCE ve exact redirect vardır. Issued client ID exchange’den önce persist edilir. RS256/JWKS signature, issuer/audience/authorized party/expiry/nonce/subject doğrulanır. Identity issuer/client/subject’tir; email authority değildir.

Credential source/task store’dan ayrıdır. Generic directory/Git capture credentials.bin/auth.lock/.auth-* path segmentlerini (tracked dahil) dışlar; candidate mutation ve rehashed manifest ile de içeri alınamaz. Bu filename filtresi genel privacy lineage yerine geçmez. Windows user DPAPI + private ACL; Unix 0700/0600 owner-only file kullanır, at-rest encryption iddiası yoktur. Owner lock refresh’i serialize eder; atomic replacement bütün token setini birlikte yayınlar. Refresh’te scope omitted önceki grant’i korur; explicit empty scope eski plan iznini koruyamaz. Logout local tokens temizler; remote revocation confirmed ayrı bool’dur.

Task AuthDirectory/AuthProfile sadece lookup handle’dır; compiler bunları context’e koymaz. Runtime explicit remote consent + exact saved account + güncel visible catalog ister. SIWC store:false/stream:true ve namespace functions kullanır. Tool delta dispatch edilmez; terminal receipt gerekir. Partial/failed/disconnected response completion değildir. Registered gpt-6.1-sol profile yayımlanmış 128000 output ceiling reserve eder; yasak max_output_tokens/truncation gönderilmez. Context conservative byte profile’dır; tokenizer calibration değildir.

## API-key runtime ve no-dispatch

OpenAI/Anthropic fixed official origin + fixed environment handle + explicit remote consent kullanır. Secret value process-local olup journal/context/export’a yazılmaz. Operator model/context/output profili declared’dır; gerçek acceptance ayrıca gerekir. Default JSON adapter ile explicit --stream API/local seçimi aynı durable loop’a bağlıdır. Stream flag canonical request ve immutable runtime profile’ın parçasıdır. OpenAI SSE sealed terminal Response’u, Anthropic SSE ordered content/fragment/signed thinking + cumulative usage’ı, Ollama NDJSON complete structured call chunks + done marker’ı doğrular. 8 MiB wire, 32768 event, 2 MiB line, bounded content/args ve 32 tool batch limitleri vardır. Terminalden önce veya failed/incomplete response sonrası tool dispatch yoktur. Mode/profile değişimi, kesik payload ve provider fallback fail-closed olur. Cancel sonrasında alınmış raw prefix ve UNKNOWN reserve kalır; fresh restore effect çalıştırmaz. UI live delta/reconnect henüz yoktur.

PreflightFailure yalnız inference HTTP transport çağrılmadan oluşur. Kernel request/profile/provider/model bound receipt üretir. Ledger KERNEL_NO_DISPATCH yalnız Used=0 settlement kabul eder; retained receipt fresh restore’da revalidate edilir. Provider bu kernel receipt’ini oluşturamaz. Dispatch sonrası belirsizlik UNKNOWN reservation korur.

## Açık kabul sınırları

Offline hostile OAuth/JWT/PKCE/refresh/revoke/catalog, SSE/namespace/admission/receipt, durable fixture/loopback ve actual Docker computation testleri vardır. Gerçek ChatGPT giriş/inference, OpenAI/Anthropic API ve kurulu Ollama model kabulü NOT_RUN’dır. Native macOS COMPILE_ONLY. Trusted observer/discovery, semantic review, full fencing, deletion/control reserve, live apply/restore, supervisor/TUI, compaction ve release eval açıktır.

SQLite schema 2 korunur. Yeni opsiyonel fields ve KERNEL_NO_DISPATCH usage source older strict decoder/reducer ile downlevel uyumlu değildir.

Resmî kaynaklar: [SIWC login](https://developers.openai.com/siwc/token-sharing-open-source/sign-in), [inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference), [profiles](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions), [limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations), [model ceiling](https://developers.openai.com/api/docs/models/gpt-6.1-sol).

Protocol kaynakları: [OpenAI stream](https://developers.openai.com/api/docs/guides/streaming-responses), [Anthropic stream](https://platform.claude.com/docs/en/build-with-claude/streaming), [Ollama stream](https://docs.ollama.com/capabilities/streaming).
