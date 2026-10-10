# PRD anahtarsız tamamlama çalışma planı

10 Ekim 2026. Yetkili kapsam [prd.md](../prd.md); başlangıç revision `50176ff`. Bu plan 9 Ekim mimari/güvenlik incelemesini çalışan kod, kullanıcı akışı ve kabul kanıtıyla kapatır. Eski planlardaki tarihsel kanıt güncel capability gibi yorumlanmaz.

## Tamamlanma ölçütü

Her paket gerçek CLI/owner akışına bağlı uygulama, saldırgan ve recovery kabulü, dürüst capability çıktısı, güncel kılavuz ve exact revision CI kanıtı gerektirir. API anahtarı gerektirmemesi bir işin yalnız kodla kabul edilebileceği anlamına gelmez. Native backend, gerçek kullanıcı login, signing identity ve bağımsız pilot ayrı kabul türleridir. Kullanıcı 10 Ekim'de geçici privileged engine ölçümünü onayladı: rootful restart/fencing geçti, bu hosttaki rootless profil kaynak controller'ları yokluğu nedeniyle reddedildi.

| Paket | Bağımlılık | Çalışan sonuç ve kabul | Durum |
|---|---|---|---|
| K01 CI/güvenlik | Yok | Go 1.27.2; üç OS full test/vet/vulnerability; Linux race/tüm fuzz target'ları; migration; actual rootful Docker; package-bound mandatory evidence; aggregate foundation | `6f0c529` ve `0139535` exact revision bütün yedi foundation job'ında GREEN; yeni runtime değişikliklerinde tekrar ölçülür |
| K02 lisans/vault/docs | K01 | Kullanıcının seçtiği MIT; Unix helper environment allowlist; actual Secret Service/Keychain unique-item roundtrip; çelişkili plaintext/fencing/eval belgeleri düzeltilir | MIT/native roundtrip PASS; ancestor trust, bounded pipe/timeout/protocol ve rotation failure matrix kodlandı; native locked/multi-user kabulü ayrı |
| K03 owner retrieval cache | K01 | Task/candidate/policy/source/version binding; fresh hash validation; single build; aktif retired lease quota; TTL/LRU/invalidation; gerçek owner warm benchmark | Kodlandı; Windows kabulü geçti |
| K04 relevance/spans/ölçek | K03 | Body/path ayrımı; dosya başına üç örtüşmeyen span; truncation; real repo recall@5/correct span/stale tests; OS peak resident ölçümü; büyük repo partition/coverage | Bounded capture'da lazy partition/coverage kodlandı; exact revision kabulü tekrar ölçülür |
| K05 delivery | K01/K02 | B/C/U üç yönlü merge preview; overlap conflict; merged candidate reverify; source-bound plan; güçlü backend admission; file intent/receipt/recovery/dedup; yalnız owned diff restore | Preview/portable merged export ve fresh fixed-check task akışı kodlandı; exclusive live backend/owned restore açık |
| K06 privacy/retention | K01 | Task-content tombstone; minimal audit ve ledger korunumu; parent/derived task lineage; content purge; managed backup watermark/purge; eski restore reddi; crash recovery | Task deletion/ordinary derivative/restore scope ve pre-journal attempt ve ayrı-owner evaluator lineage/report/restore kapsamı kodlandı; TTL ve metadata/content-wide açık |
| K07 resources/fencing | K01/K06 | Managed copy/metadata fiziksel quota; atomic fence admission; lifetime/restart/rootless/backend acceptance; UNKNOWN doğru korunumu | Metadata reserve ve rootful restart/fencing kabulü var; managed payload toplam quota/lifetime ve desteklenen rootless native kabulü açık |
| K08 verification | K01/K05 | Check revision/discovery/protected dependency closure; V0–V5/language boundaries; protected external oracle; negative forged report fixtures | STDIO observer var; genişleme açık |
| K09 supervisor/TUI | K05/K06/K07 | Retention gap/resync; provider delta projection; recovery/restore UI; queue goal scheduling; native PTY/hostile child acceptance | Kısmen uygulandı |
| K10 policy/extensions | K02/K06/K07 | Admin/current restriction revision; ayrı privacy axes; pinned isolated extension/MCP runtime; bounded message/egress/cancel/resource enforcement | Açık |
| K11 eval/performance | K03/K04/K08 | Frozen preregistered dataset runner; condition/build attestation; bütün attempts/UNKNOWN costs; holdout/paired continuation; reproducible reference p95 | Separate-owner eval var; genişleme açık |
| K12 install/release | K01/K02/K05/K06/K10/K11 | Platform packages; exact revision signed/checksummed inventory; install/update/rollback; release manifest; independent pilot/adoption | Reproducible bundle var; genişleme/kabul açık |

Kodlama sırası K02–K04 → K05 → K06 → K07/K08 → K09/K10 → K11/K12. Her sınır değişikliği önce kernel/admission/replay seviyesinde çözülür; arayüz geçersiz assurance üretmez. K01 kırmızıysa yeni release capability eklenmez; önce kırmızı test düzeltilir.

## Delivery için teknik karar

Normal filesystem advisory lock'u, hash kontrolü ile write arasındaki dış writer yarışını kapatmaz. Windows share-mode/file lock kapsamı ayrıca existing mapped writer ve parent/namespace değişiminde ölçülmelidir. Güçlü exclusivity kanıtı olmadan auto-apply açılmaz. Böyle bir backend için PRD §13.3'ün güvenli yolu candidate/patch teslimidir; approval bu assurance'ı üretmez. Merge preview ve immutable merged candidate, canlı dosyaya yazmadan geliştirilebilir ve yeniden doğrulanabilir.

## Kanıt türleri

- **Fixture/unit:** Contract, stale policy, forged report, retry, lineage ve crash state için.
- **Gerçek native kabul:** Kernel/filesystem/vault/backend/process davranışı için; mock yerine geçmez.
- **Anahtar istemeyen dış kabul:** ChatGPT login/consent, local hardware/model, signing identity, kullanıcı pilotu; desteklenen rootless/cgroup delegation profili üzerinde native kabul.
- **API anahtarı isteyen kabul:** OpenAI/Anthropic gerçek protocol/usage/quota/rate/cancel ve model kalitesi. Offline fixtures gerçek provider kabulü diye sayılmaz.

İlk stable A–D ile E–G deneyleri ayrı gate taşır. Kullanıcının bütün PRD vizyonu talebi için FR-28..42 deneyleri de kendi bağımlılık/egress/eval guard'larıyla ele alınır; kapalı deney `DONE` olarak işaretlenmez. PRD yüzdesi test sayısından hesaplanmaz ve dış kabul olmadan `release_ready:true` üretilmez.

## 10 Ekim teknik denetiminin plana işlenmesi

[Güncel kodla karşılaştırma](AUDIT_RECONCILIATION_2026_10_10.md) rapordaki eski CI/vault durumunu ve zaten uygulanmış alt işleri ayırır. F-01–F-08 tam olarak kapanmış sayılmaz. Foundation sonrası öncelik K06 metadata lifecycle → K07 bütün managed payload writer'larında ortak fiziksel budget → K09 kapasite remediation/K11 legacy inventory → K05 exclusive live delivery şeklindedir. Mevcut preview/reverify ve native vault dilimleri yeniden kodlanmaz.

K06 baseline: gerçek 2048 journal/64 MiB ve 8192 katalog sınırları; 0/256/1024/1536/2048 replay/admission ölçümü; 10 reopen/replay; reserve/genesis/bind/scope/checkpoint publication fault sınırları. Checkpoint current external authority, hash-chain continuity, deletion watermark, old-backup rejection, lineage ve UNKNOWN/recovery pin'lerini korumadan yayınlanmaz. Allocation kaydıyla ve exact physical-root kanıtıyla bağlanmamış authority artıklarının inventory/cleanup kabulü ayrı gerekir. Prefix veya dizin yaşı silme yetkisi değildir.

K07 baseline: backup/export/restore/evaluator/report dahil tüm staged/final/temp/outstanding payload'ların writer inventory ve admission/settlement sözleşmesi; eşzamanlı exhaustion, failed writer ve restart recovery; bounded disk-pressure ve kontrol yolunun korunması. Native power-loss ve desteklenen rootless kabulü fixture/unsupported sonuçlarıyla kapatılmaz. F-06 numeric status gelecekteki izin değildir; F-08 schema-1 inspect otomatik lineage migration değildir.

[Detaylı PRD teslim planı](KEYLESS_DELIVERY_PLAN.md) bütün FR-01–42/NFR-01–12 için uygulama/kullanıcı akışı/negatif kabul bağımlılıklarını taşır. İlk yeni dilim [ADR 0016](adr/0016-privacy-authority-allocation.md): reserve/genesis öncesi exact physical-root allocation CAS; bind ile atomik promotion; actual child death ve restart reuse. 2048 gerçek registry kaydı/2049 deny/on replay, 8192 gerçek owner katalog baskısı ve fiziksel metadata byte-class saturation fixture'ları eklendi; bunlar compaction veya toplam managed payload quota değildir. Exact final kaynak/CI kanıtı ayrıca kaydedilir.
