# PRD anahtarsız tamamlama çalışma planı

10 Ekim 2026. Yetkili kapsam [prd.md](../prd.md); başlangıç revision `50176ff`. Bu plan 9 Ekim mimari/güvenlik incelemesini çalışan kod, kullanıcı akışı ve kabul kanıtıyla kapatır. Eski planlardaki tarihsel kanıt güncel capability gibi yorumlanmaz.

## Tamamlanma ölçütü

Her paket gerçek CLI/owner akışına bağlı uygulama, saldırgan ve recovery kabulü, dürüst capability çıktısı, güncel kılavuz ve exact revision CI kanıtı gerektirir. API anahtarı gerektirmemesi bir işin yalnız kodla kabul edilebileceği anlamına gelmez. Native backend, gerçek kullanıcı login, signing identity ve bağımsız pilot ayrı kabul türleridir. Kullanıcının rootless/gerçek engine restart ölçümlerini bekletme kararı sürer.

| Paket | Bağımlılık | Çalışan sonuç ve kabul | Durum |
|---|---|---|---|
| K01 CI/güvenlik | Yok | Go 1.27.2; üç OS full test/vet/vulnerability; Linux race/tüm fuzz target'ları; migration; actual rootful Docker; aggregate foundation | Başlangıç revision GREEN; yeni revision tekrar ölçülür |
| K02 lisans/vault/docs | K01 | Kullanıcının seçtiği MIT; Unix helper environment allowlist; actual Secret Service/Keychain unique-item roundtrip; çelişkili plaintext/fencing/eval belgeleri düzeltilir | MIT/hardening/native roundtrip PASS; geniş vault failure matrix ayrı |
| K03 owner retrieval cache | K01 | Task/candidate/policy/source/version binding; fresh hash validation; single build; aktif retired lease quota; TTL/LRU/invalidation; gerçek owner warm benchmark | Kodlandı; Windows kabulü geçti |
| K04 relevance/spans/ölçek | K03 | Body/path ayrımı; dosya başına üç örtüşmeyen span; truncation; real repo recall@5/correct span/stale tests; OS peak resident ölçümü; büyük repo partition/coverage | İlk bölüm kodlandı; partition açık |
| K05 delivery | K01/K02 | B/C/U üç yönlü merge preview; overlap conflict; merged candidate reverify; source-bound plan; güçlü backend admission; file intent/receipt/recovery/dedup; yalnız owned diff restore | Preview/portable merged export kodlandı; live backend ve reverify akışı açık |
| K06 privacy/retention | K01 | Task-content tombstone; minimal audit ve ledger korunumu; parent/derived task lineage; content purge; managed backup watermark/purge; eski restore reddi; crash recovery | Task deletion/ordinary derivative/restore scope kodlandı; TTL, metadata/content-wide, eval lineage açık |
| K07 resources/fencing | K01/K06 | Managed copy/metadata fiziksel quota; atomic fence admission; lifetime/restart/rootless/backend acceptance; UNKNOWN doğru korunumu | Kısmen uygulandı; kullanıcı ertelemesi var |
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
- **Anahtar istemeyen dış kabul:** ChatGPT login/consent, local hardware/model, signing identity, kullanıcı pilotu; rootless/restart kullanıcı tarafından ertelendi.
- **API anahtarı isteyen kabul:** OpenAI/Anthropic gerçek protocol/usage/quota/rate/cancel ve model kalitesi. Offline fixtures gerçek provider kabulü diye sayılmaz.

İlk stable A–D ile E–G deneyleri ayrı gate taşır. Kullanıcının bütün PRD vizyonu talebi için FR-28..42 deneyleri de kendi bağımlılık/egress/eval guard'larıyla ele alınır; kapalı deney `DONE` olarak işaretlenmez. PRD yüzdesi test sayısından hesaplanmaz ve dış kabul olmadan `release_ready:true` üretilmez.
