# 10 Ekim teknik denetiminin güncel kodla karşılaştırması

Kullanıcının ilettiği **VIBER | Teknik Denetim ve Yol Haritası** raporu `6f0c5294c160a79ec0aa9c2ac55c38911ab91bb4` revision'ını inceliyor. Bu karşılaştırmanın kod tabanı `013953593fb58a71fc5c2a8987658f7a1f2b600c`; sonraki belge commit'i yeni runtime capability sağlamaz. Yerel kaynak okuması ve exact-SHA GitHub CI durumu ayrı değerlendirildi. Rapor bir pentest veya fiziksel disk tükenmesi kabulü olarak yorumlanmadı.

## Yeniden yapılmayacak işler

| Rapordaki madde | Güncel durum ve kanıt | Karar |
|---|---|---|
| `6f0c529` Docker/aggregate CI henüz bitmedi | [Exact-SHA CI 38033874492](https://github.com/ixayldz/Viber/actions/runs/38033874492) tamamlandı; yedi job SUCCESS. [Doğrulama kaydı](VALIDATION.md) bunu zaten içeriyor. | Eski bekleme durumu kapandı. Aynı değişiklik tekrar kodlanmaz. |
| Ayrı work/control metadata sınıfları, 32 MiB fiziksel reserve, owner admission, handle-bound volume sorgusu | [Kapasite kodu](../internal/agent/privacy_capacity.go), [native platform testleri](../internal/diskguard/reserve_test.go), [gerçek journal/catalog baskısı testleri](../internal/agent/privacy_capacity_test.go) mevcut. | Mevcut sınırlı garantiyi koru; global payload quota diye sunma. |
| Separate-owner evaluator ve managed purge | [Evaluator](../internal/evaluation/native.go), [durable registry](../internal/agent/privacy_registry.go) ve [kabul kaydı](VALIDATION.md) mevcut. | Yeni schema-2 bundle lineage'ını yeniden yazma. Legacy migration ayrı açık. |
| Geniş vault hata matrisi eksik | Rapor sonrasında `d9449ea`/`0139535`: noisy/noncanonical/truncated response, trusted executable ancestors, gerçek subprocess overflow/deadline/inherited pipe ve rotation failure testleri eklendi. [Test kaynağı](../internal/auth/vault_failure_unix_test.go). `0139535` native Linux/macOS vault job'ları SUCCESS. | Fixture/protocol dilimi tamamlandı. Gerçek locked desktop/multi-user kabulünü tamamlanmış sayma. |
| Zorunlu test gate'leri | `0139535` 66 zorunlu parent testini canonical Go package kimliğiyle bağladı; foreign same-name PASS ve subtest-only PASS reddediliyor. [Validator](../scripts/verify-test-results.py), [dokuz black-box kontrat](../scripts/test-evidence-validator.py). | Mevcut zorunlu testler tekrar yazılmaz; yeni kabul parent'ları açık identity manifest'i gerektirir. |
| B/C/U preview, immutable merged candidate ve fresh check | [Plan K05](KEYLESS_COMPLETION_PLAN.md) ve [kabul kaydı](VALIDATION.md) mevcut. | Candidate/patch teslimi korunur; exclusive live apply ve owned restore açık kalır. |

`d9449ea` CI macOS cleanup aşamasında başarısız oldu; önceki başarılı Put/Get bu revision'ı GREEN yapmaz. `0139535` cleanup doğal process/pipe completion ve fresh unique-item absence ister. Bu kontrol yeni native macOS vault job'ında geçti. Karşılaştırmanın final kontrolünde [exact `0139535` CI 38035652309](https://github.com/ixayldz/Viber/actions/runs/38035652309) bütün yedi job'da SUCCESS: üç OS core, gerçek rootful Docker, iki native vault ve aggregate foundation. Bu runtime revision kanıtıdır; sonraki belge commit'i için aynı SHA iddiası yapılmaz.

## F-01–F-08: kabul edilen kalan işler

| Bulgu | Mevcut kanıt / gerçek sınır | Paket ve tamamlanma koşulu |
|---|---|---|
| **F-01 — Sonlu journal** | 2048 slot ve 64 MiB sınır reddi unit kontratında mevcut; 1536 gerçek synced ordinary record sonrası deletion purge de mevcut. Bunlar 2048 gerçek record altında lifecycle/compaction değildir. | **K06/K07:** tam fiziksel saturation, watermark/deletion/lineage eşdeğer replay, 10 ardışık reopen/replay ve checkpoint publication crash suite; compaction öncesi authority/restore ADR'si. |
| **F-02 — Admission/replay tarama maliyeti** | `readPrivacy` zinciri doğruluyor; `privacyMetadataBytes` ayrı inventory tarıyor. Ölçülmüş 0/256/1024/1536/2048 eğrisi yok. | **K06/K11:** sabit fixture/veri boyutu ve referans host ile latency/okunan byte/işlem ölçümü. Snapshot/index yalnız integrity ve current watermark doğrulamasını koruyorsa kullanılabilir. Ölçmeden performans hedefi geçildi denmez. |
| **F-03 — Toplam managed-copy fiziksel quota** | Tek kopya bounds ve metadata reserve mevcut; backup/export/restore/evaluator/report toplam payload admission'ı yok. | **K07:** bütün writer'larda write-before-admission yasağı; outstanding/temp/final payload muhasebesi; failed/crashed writer settlement; aynı authority altında eşzamanlı exhaustion ve deletion kontrol yolu kabulü. |
| **F-04 — Disk pressure/power-loss matrisi** | Root substitution, oversized reserve ve process crash fixture'ları gerçek power-loss değildir. | **K01/K06/K07/K12:** disposable test filesystem'de bounded disk pressure; publish/sync/rename/bind sınırlarında ayrı process-kill/reopen kanıtı; ayrıca native filesystem ve power-loss kabulü. Kill fixture'ı power-loss diye adlandırılmaz. Kullanıcının ertelediği privileged engine ölçümleri bu kapsamla çalıştırılmaz. |
| **F-05 — Bind öncesi orphan authority** | `ensurePrivacy` fresh external directory ve reserve oluşturup sonra `BindPrivacyAuthority` çağırıyor; hata yolunda yalnız root close var. Kaynak gözlemi doğrulandı; fault injection ile yeniden üretim henüz yok. | **K06/K07:** bind-before/after crash ayrımı; immutable allocation ownership ve exact physical-root inventory. Cleanup için current journal binding/lease/scope kanıtı gerekir; yalnız `.viber-privacy-*` prefix'i, yaş veya boş dizin yetki sağlamaz. Foreign/replaced/bound root asla silinmez. |
| **F-06 — Capacity remediation UX** | Numeric `privacy_metadata_capacity` ve `work_ready` var; bunlar anlık gözlem. CLI/TUI self-service remediation akışı tamamlanmış değil. | **K09:** work admission durduğunda nedeni, desteklenen güvenli next action ve retry sonucu görünür; raw path/task/secret sızıntısı yok. Status deletion izni veya başarılı compaction sayılmaz. |
| **F-07 — Owner lifetime/katalog cleanup** | 7680 gerçek entry'de yeni owner deny, mevcut owner reopen/delete PASS. Eski scope/lease entry'lerinin güvenli retirement mekanizması yok. | **K06/K07:** aktif lease, restored/evaluator family, old backup ve process fence pin'leri korunarak retirement; 8192 fiziksel entry sınırı ve foreign/replaced/active owner negatif kabulü. Scope kaybı old backup resurrection açmamalı. |
| **F-08 — Legacy evaluator inventory/migration** | Schema-1 inspect compatibility ve fake schema-2 upgrade reddi var. Bunlar bütün eski bundle'ları current deletion family'ye dahil etmez. | **K06/K11:** salt okunur legacy inventory; operator'ın açık kapsam seçimiyle origin/physical-root/audit-bound migration. Eski verdict yeni privacy assurance'ını kendiliğinden kazanmaz. Denetlenemeyen bundle açık unsupported kalır. |

F-01–F-08 açık bulgulardır; mevcut alt testler ilgili family'nin tamamını kapatmaz. F-05 doğrulanmış exploit diye işaretlenmedi. Rapordaki 2048/64 MiB/8192 sayısal sınır testleri ile gerçek saturation/crash ölçümleri birbirinden ayrılır.

## Uygulama sırası ve güvenlik koşulları

1. Exact `0139535` foundation sonucu tamamlandı: bütün yedi job SUCCESS. Gelecekte herhangi bir kırmızı job önce düzeltilir; kırmızı CI üzerine yeni release capability eklenmez.
2. **K06 lifecycle:** F-01/F-02/F-05/F-07 için replay/saturation/fault baseline ve compaction ADR'si. Checkpoint task-context compaction ile aynı şey değildir. External current authority, sequence/tail, deletion watermark, lineage ve restore admission korunur; eski backup eski checkpoint'e dönerek silinmiş içeriği yeniden açamaz. Eski metadata ancak yeni checkpoint'in dayanıklılığı ve recovery kabulü kanıtlandıktan sonra kaldırılır.
3. **K07 ortak payload budget:** F-03/F-04; tam writer inventory, staged/final accounting ve bounded control reserve. Sonlu reserve her gelecekteki deletion için sınırsız garanti sağlamaz.
4. **K09/K11:** F-06 remediation ve F-08 legacy inventory/migration; K06/K07'nin mevcut authority/ledger sözleşmesini kullanır.
5. **K05 live delivery:** exclusive backend capability, current source precondition, per-file intent/receipt ve yalnız owned diff restore. Advisory lock/hash-check veya kullanıcı onayı exclusivity sağlamaz. Ölçülmeyen backend candidate/patch teslimini kullanır.
6. **K08/K11/K12:** generic verifier, önceden kayıtlı benchmark/holdout, platform dağıtımı ve bağımsız pilot. Engineering bundle imzalı stable release sayılmaz.

## Anahtar ayrımı

**API anahtarı gerektirmeyen geliştirme:** F-01–F-08; K05 exclusive backend/restore; verifier, supervisor/TUI, isolated MCP/extensions; frozen dataset runner, build/condition attestation ve install/update/rollback kodu. Bunların tamamı henüz kapanmış değil.

**API anahtarı istemeyen dış kabul:** native locked/multi-user vault, filesystem/power-loss, gerçek ChatGPT account login/consent, kurulu yerel model/donanım, signing identity ve bağımsız kullanıcı pilotu. Bunlar fixture PASS ile kapatılamaz. Rootless/gerçek engine restart kullanıcı kararıyla bekliyor.

**API anahtarı gereken kabul:** gerçek OpenAI/Anthropic protocol/usage/rate/cancel/charge senaryoları ve bu API modelleriyle preregistered kalite/ablasyon deneyleri. Offline fixture'lar bu kabulün yerine geçmez.

Rapor yeni bir PRD yüzdesi hesaplamak için yeterli veri sağlamıyor. [Anahtarsız tamamlama planı](KEYLESS_COMPLETION_PLAN.md) ve [release gate'leri](RELEASE_GATES.md) geçerliliğini koruyor; `%100`, `release_ready:true` veya “yalnız API anahtarı işleri kaldı” sonucu çıkarılmadı.
