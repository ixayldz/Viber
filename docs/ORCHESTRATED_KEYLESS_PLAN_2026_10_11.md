# Anahtarsız PRD uygulaması — orkestrasyon planı

11 Ekim 2026. Kaynak başlangıcı `4d58d7e`; runtime `f1b323d`. Yetkili kapsam bütün PRD'dir. Girdi: [kendi kapsam denetimi](PRD_AUDIT_2026_10_11.md) ve kullanıcının 11 Ekim bağımsız analizi. Tamamlanma yalnız çalışan entegrasyon ve uygulanabilir kabul ile kaydedilir; yeni interface, plan veya unsupported yanıtı özelliği tamamlamaz.

## Rapor karşılaştırması

İki raporun ortak açıkları: metadata checkpoint/compaction; aggregate fiziksel kota; exclusive apply/owned restore; genel protected verification; gerçek coding/eval kabulü; update/rollback ve bağımsız pilot. Rootless profili, Windows ACL regresyonu, durable retention scheduler, Go declaration AST ve portable engineering dağıtımı zaten uygulanmıştır. Bunları yeniden yapmayacağız. Bağımsız rapordaki README/INSTALL eski release bağlantısı bulgusu `4d58d7e` ile düzeltilmiştir; rapor runtime `f1b323d` üzerinden değerlendirildiği için bu belge düzeltmesini kapsamamaktadır.

## Eşzamanlı dosya sahipliği

| Hat | Ajan | İlk teslim |
|---|---|---|
| Metadata/privacy | Luna max / privacy_lifecycle | Current authority'ye bağlı checkpoint/migration/retirement; watermark/dedup/UNKNOWN/lineage korunması |
| Kaynak bütçesi | Luna max / physical_quota | Durable aggregate admission ve gerçek writer entegrasyonları |
| Delivery | Luna max / delivery | Kullanılabilir apply preflight/conflict/receipt/fallback; teknik exclusivity olmadan live write açılmaması |
| Verification/intelligence | Luna max / verification_intelligence | Bounded framework discovery, source/dependency closure ve graph coverage entegrasyonu |
| Supervisor/UX | Luna max / supervisor_ux | Durable goal/cursor, ephemeral delta/gap/resync ve bounded client akışı |
| Entegrasyon/dağıtım | Ana ajan | CLI bağlantıları, installer/updater/rollback, kaynak incelemesi ve birleşik kabul |

Ajanlar aynı dosyaları eşzamanlı değiştirmez. `internal/cli/cli.go` ana ajana aittir. User'ın silmeleri ve `docs/PRD_STATUS_ANALYSIS.md` değişikliği korunur. Scoped testler sınırlı paralellik ile yürütülür; tek birleşik full/race/kabul turu yapılır.

## Bütün kalan kapsamın teslim sırası

1. **Çekirdek:** metadata/audit lifecycle; kalıcı owner ve bütün legacy scope migration; ortak temp/final/outstanding disk muhasebesi; uygulanabilir safe delivery profili; protected verifier closure.
2. **Günlük ürün:** goal queue/scheduling; streaming/cursor gap/resync; native TUI recovery; current admin revision/privacy axes; taze kurulum/update/reinstall/rollback/store-format compatibility.
3. **E–G:** source-bound graph/history/co-change/local semantic/reranker; runtime conservative test selection; blueprint generation/protected eval; typed multi-worker DAG/AgentLease/mutex/integration/shared budget; measured routing/external worker; isolated pinned MCP/extensions; versioned skills; opt-in training exports; learned shadow/canary/rollback; team/remote executor.
4. **Bağımsız kabul:** preregistered dataset/condition/build/holdout runner, bütün attempt/UNKNOWN maliyeti, reference performance ve yerel modeller için actual endpoint kabulü. Kullanılabilir gerçek ortam olmadığı yerde NOT_RUN açık kalır.
5. **Teslim:** exact source full CI, scoped saldırgan/crash regresyonu, native paket akışı ve anonim yayın doğrulaması; kullanıcı kılavuzu/capability matrisi güncellemesi.

## Hız ve doğruluk sınırı

Kullanıcı gereksiz test yazımını atlamayı onayladı. Düşük etkili UI/doküman değişikliklerine yeni test zorunlu değil. Silme/kota/migration/apply/authority kodunda veri koruma ve stale admission doğrulaması gerekir. Test sayısı, kapalı backend veya fixture model bir ürün kabulü yerine sayılmaz. Anahtarsız dış hesap/model/signing/hardware/insan kabulü API-key işi diye yeniden etiketlenmez.

## Gerçek ortam ayrımı

API key isteyen kabul: OpenAI/Anthropic actual dispatch/protocol/usage/model-quality. API key istemeyen dış kabul: eligible SIWC hesap ve consent, yerel model weights/hardware, OS signing identity, independent human pilot ve remote host/tenant. Yazılım ve fixtures bu dış ortamlar beklenmeden uygulanır; kullanılamayan gerçek ortam için PASS üretilmez.

## Sonuç kaydı

Her teslim aşağıdakileri ekler: değişen dosya ve kullanıcı komutu; kaynak/policy/digest bağları; çalıştırılan scoped test/build sonucu; entegrasyon durumu; gerçek kabul sınırı. Kapsamın tamamı kapanmadan bu belge veya release `DONE/%100` olarak işaretlenmez.

## Bu uygulama turunun teslimi

| Teslim | Çalışan yüzey | Kabul sınırı |
|---|---|---|
| Gizlilik checkpoint | `privacy-checkpoint`; owner SQLite PREPARED/ACTIVE witness, lossless projection, exact receipt ile kaynak journal retirement, ikinci checkpoint/reopen | Yeni owner ve restore/move rebase, eski checkpoint/catalog retirement tamamlanmadı |
| Aggregate fiziksel kota | CAS/materialization, snapshot, backup/export ve evaluator bundle reserve/WRITING/measured receipt | Bütün mutable metadata/fence yazıcıları ve her FREE_PENDING kapasite dönüşü kapanmadı |
| Supervisor | Durable goal queue, sabit child kimliği, parent privacy lineage, gerçek provider delta/gap/resync | Çoklu worker DAG/lease/mutex/shared parent budget yerine geçmez |
| Intelligence/verification | TS/JS/Python bounded framework/import keşfi, protected closure; cutoff-safe loose Git history/co-change; source-bound local semantic ranking | AST/LSP resolved graph, packed history ve actual runtime coverage kabulü tamamlanmadı |
| Deney kaydı | Dataset/condition/build/source/recipe inventory; dispatch öncesi single-use STARTED ve owner-bound rapor | Actual bağımsız model cohort, reference p95 ve pilot değildir |
| Operatör politikası | Source dışında pinned revision/digest; güncel effect/provider gate ve aktif inference iptali | Centrally authenticated team admin/remote executor değildir |
| Eğitim export | Ayrı kullanıcı opt-in ve task/document/candidate/exact samples consent, license/provenance, kota ve deletion lineage | Operatörün incelenmiş sample beyanı bağımsız gold/eval etiketi değildir; egress yok |
| Yönetilen kurulum | Exact GitHub provenance, native archive inventory, kayıtlı store owner locks, install/reinstall/rollback/deactivate/owned uninstall, crash resume | İki farklı revision upgrade ve platform sertifika/notarization kabulü henüz yok |
| Delivery | Güncel merge/conflict/preflight paketi ve açık `target_written=false` | Strong exclusive live apply/owned restore backend tamamlanmadı |

Native Windows scoped migration/privacy/goal/training/distribution kabulü, native build, `go vet` ve gofmt geçti. Son Go çalışma ağacının `go test -p 2 -json -count=1 -timeout 15m ./...` turu 11 Ekim 2026 16:41 TRT'de hatasız tamamlandı; log `.cache/orchestration-acceptance.jsonl`. Python yayın/paket trust sözleşmelerinin 90 kontrolü geçti; bunlar actual OIDC/native installation kabulü değildir. Linux race/fuzz/Docker/rootless, Unix vault ve üç native installer için yeni 16 işlik exact-source CI kabulü ayrıca gerekir. Önceki yayımlanmış sürümün yeşil CI'si bu değişiklikleri kabul etmiş sayılmaz.
