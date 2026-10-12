# Anahtarsız PRD uygulaması — orkestrasyon planı

## 12 Ekim teslim ilerlemesi

İlk managed [public engineering sürümü](https://github.com/ixayldz/Viber/releases/tag/engineering-90bc97ebc9318e3542ffbc2507b8cb346dae69e5) yayımlandı: exact source `90bc97ebc9318e3542ffbc2507b8cb346dae69e5`, 16/16 CI SUCCESS, üç native yönetilen kurulum/ilk offline görev/reinstall/aynı revision rollback/owned uninstall ve dokuz OIDC subject kabulü. Public 11 dosya oturumsuz indirme/checksum/provenance readback PASS. Gerçek Docker MCP ve pinned Go helper kabulü de geçti. Sealed parent budget/child allocation ve Windows bounded exclusive NTFS profili aynı kaynağın kabulüne dahildir; bunlar aşağıdaki ilk planın açık satırları olarak yeniden yapılmaz.

Bu sürüm sonraki kaynak CI'ının cross-revision baseline'ıdır. Bir sonraki dağıtım kabulü mevcut görev projection'ını koruyan compatible upgrade → rollback → upgrade ölçer; iki-revision PASS henüz bu ilk yayına atfedilmez. README, INSTALL ve CAPABILITIES güncel yayına bağlandı. Gereksiz yeni/full yerel test eklenmedi; zorunlu mevcut CI kapıları korundu.

Kalan geniş PRD kapsamı: bütün catalog/anchor/UNKNOWN/legacy owner yaşam döngüsü; bütün mutable metadata/fence fiziksel writer profilleri; genel protected verifier/runtime coverage ve diğer dillerde LSP; typed multi-worker DAG/AgentLease/mutex/integration ve measured routing; authenticated team/remote executor; bağımsız effectiveness/pilot/reference performans. Gerçek eligible ChatGPT hesap/model ve Ollama endpoint kabulü de anahtarsız dış kabul olarak açık kalır. OS certificate/notarization kimliği API anahtarı işi değildir. Yalnız gerçek ücretli OpenAI/Anthropic provider kabulü model API anahtarı bekler. Bu belge %100/stable tamamlanma beyanı değildir.

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

## 12 Ekim devam teslimi

Cold retrieval V2, 10s runtime build deadline'ını koruyarak 128-file lazy batch kullanır; 10.000-file complete partition planı ve owner cache/span/regression kontrolleri geçti. Önceki `4fa2332` Windows/macOS/rootless SUCCESS olsa da Ubuntu race fallback nedeniyle aggregate CI FAILED, bu SHA yayımlanmadı.

`control.reserve` fiziksel blokları ayrı PAYLOAD high-water + standby ilişkisinde sayılır; ControlBytes emergency slack değildir. NTFS actual 384KiB allocation overhead ölçüldü; aynı 512KiB bounded allowance hem admission envelope hem metric'te uygulanır. Risk-reconciliation restore/reopen focused kontrolleri geçti. Owner-pinned lease'ler ve full anchor chain korunur; yalnız unlocked, unpinned boş legacy lock reclaim edilir. Foreign nonempty legacy descriptor sessizce adopte edilmez.

Parent/child work pool: TaskCreated sealed maxima/counter witness, immutable per-goal allocation digest, reducer sum/admission ve Document readback; source parent holds ve in-flight risk birlikte hesaba katılır. Legacy child inspection-only, hiçbir retry yeni allocation üretmez. Gerçek multi-worker DAG/AgentLease/mutex/fabric'in bütün kapsamı yerine geçmez.

Go graph host default parse-only/PARTIAL/broad checks; unsafe synchronous type-check normal tool yolundan çıkarıldı. `--go-graph-runtime` source-bound fixed helper için pinned image, durable lease/capsule ve bounded container profiline bağlanır; standalone `--cooperative-types` açık ayrı host tercihtir. Helper protocol analizi adlandırır, limit garantisi üretmez; limit/quiescence otoritesi runner receipt'idir. Actual Docker helper kabulü yeni exact-source CI'da ayrıca zorunludur.

Dağıtım sırası: yeni SHA 16-job kabul → ilk managed engineering public baseline → bu exact baseline'a karşı gerçek cross-revision install/rollback → kullanıcı download/install README bağlantıları. Signing, eligible SIWC/live local model, independent cohort/pilot/reference performance ve kalan geniş FR/NFR kabulü bu offline yazılım teslimlerinden çıkarılmaz.

## 11 Ekim akşam entegrasyonu

README ilk kurulum → ChatGPT hesabı → ilk görev → onay → export sırasına indirildi; ayrıntılı komutlar `CLI_REFERENCE.md` içinde kaldı. Resmi SIWC OSS akışı ayrı API key/client secret istemez. Gerçek eligible hesapla giriş ve model çağrısı ölçülmedi; bu anahtarsız dış kabul hâlâ açıktır.

Restore/checkpoint rebase, exact owned stage recovery, sibling export koruması, live-operation checkpoint gate, FREE_PENDING durable replay ve canonical-path-bound physical quota seed uygulandı. Skills, source-bound DEV eğitimi/ayrık HOLDOUT evaluation/SHADOW-CANARY-ACTIVE/kill-rollback ve exact new-repository generation CLI'ye bağlandı. MCP protocol/duplex ve Windows sealed-file helper için odaklı kontroller geçti; Session/CLI extension ve owned live delivery entegrasyonu henüz açık.

Yerel yeni binary: `go vet` ve build geçti; mevcut offline demo ilk görev, export, backup, fresh restore, geçmiş replay ve source preservation akışını başarıyla çalıştırdı. Son exact-source CI ayrıca zorunludur. Kalan büyük anahtarsız PRD başlıkları: old catalog retirement/legacy owners, bütün mutable quota/fence writers, genel protected verifier/runtime coverage, resolved graph/packed history, typed multi-worker DAG/shared parent budget, authenticated team/remote execution, iki farklı revision upgrade ve bağımsız pilot/performance kabulü. Bunlar API key işi diye yeniden sınıflandırılmadı.

## Bu uygulama turunun teslimi

### Birleşik takip teslimi

Session/CLI MCP extension artık exact manifest ve tool-call onayıyla, read-only/network-none Docker capsule içinde çalışır. `repo-graph` ve `fs_graph` captured Go cross-file sembol ilişkilerini kullanır; generic type-check kaynakları PARTIAL/broad selection kalır. Packed Git v2 index/pack v2–v3 ve bounded delta okuyucusu bağlandı. Bunlar runtime coverage veya bağımsız kalite kabulü değildir.

Windows `apply --exclusive-ntfs`, owned `restore --checkpoint` ve read-only `delivery-reconcile` aynı terminal görev yeniden açıldığında da çalışır. Yerel fixed NTFS, mevcut single-link regular dosyalar ve içerik değişikliği sınırı vardır. Current operator `workspace.apply`/`workspace.restore` yetkisi ve exclusive handle readback zorunludur; Linux/macOS genel live backend kapalıdır.

Mutable SQLite standby/high-water kotası, pinned backup checkpoint projection temizliği ve çoklu retrieval partition cache entegrasyonu tamamlandı. Catalog/anchor history, aggregate control reserve, shared parent work-budget ve genel process-isolated semantic CPU sınırı ayrı açıklar olarak korunur. İki farklı managed revision için gerçek upgrade/rollback kabul yolu hazır; uygun ilk managed public baseline henüz yoktur.

Birleşik kaynak `go vet -p 1 ./...` ve CLI build PASS. İlgili native NTFS/restart/reconcile, checkpoint/backups, quota/crash, source graph/packed history ve cache kontrolleri PASS; gereksiz full yerel tekrar yapılmadı. Önceki `e342766` exact-source CI FAILURE kaydı silinmez; düzeltmeler yeni SHA'nın 16 job kabulünü bekler.

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
