# 10 Ekim güncel HEAD incelemesinin karşılaştırması

Kaynak: kullanıcı tarafından gönderilen “Viber — Güncel Durum ve Mimari Analiz”; incelenen revision `0af0d3f1cf7b237dadc63f6eabbb8870c2441916`. Rapordaki olgunluk puanları öznel değerlendirmedir; PRD tamamlanma yüzdesi veya release kabulü sayılmaz.

| Bulgu | Kod/kanıt karşılığı | Sonraki kabul |
|---|---|---|
| Windows CI kırmızı | [Run 38071476889](https://github.com/ixayldz/Viber/actions/runs/38071476889): Windows ACL fixture ACCESS_DENIED üretmedi; foundation başarısız, vault/rootful atlandı | Aynı revision için bağımsız native read + Go read + IPC discovery erişim reddi, ardından shutdown/absence/lock kabulü; yeni exact-SHA bütün CI |
| Rootless destek profili geçti | CE 28.0.4/cgroup2/systemd, gerçek restart, beş hostile alt senaryo, OOMKilled ve cleanup; önceki c63441f bütün sekiz job yeşil | Ölçülmeyen backend'ler bu kanıttan destek kazanmaz |
| Authority allocation/operation retirement | Durable pending allocation ve current authority CAS, actual crash recovery; yalnız typed transient operation lease retirement uygulanmış | Persistent owner/legacy lifecycle açık |
| Managed content retention | Explicit consent/revision/deadline, immutable intent, active/UNKNOWN/descendant pin, owner timer/CLI/TUI | Metadata/audit retention, raw redaction, checkpoint/compaction açık |
| Retention operasyonel sınırları | SYSTEM_UTC ve shared backup purge belgelenmiş; cold reopen cursor starvation ayrıca bulundu | Bounded durable scheduling CAS ve actual close/reopen adalet kabulü; kullanıcı consent metninde shared-backup kaybı görünür olmalı |
| README eski ifadeler | Content expiry/rootless ifadeleri eski kayıtlarla çelişiyor | Aktif kullanım metni ve güncel capability kaydı düzeltilir; tarihsel başarısız kanıt silinmez |
| Live apply/generic verifier | Immutable candidate/B-C-U preview/fresh check uygulanmış; teknik exclusive write ve genel framework discovery bitmemiş | Hostile writer/per-file crash/owned restore ve protected framework kabulü |
| Release/benchmark | Reproducible engineering bundle var, public signed install/update/rollback ve preregistered pilot yok | PRD A–D release gate; exact revision/native package/kurulum/güncelleme/geri dönüş/attestation/pilot |

## Yetkili teslim sırası

1. Güncel CI regresyonunu düzelt ve exact-SHA foundation yeşilini doğrula.
2. Metadata checkpoint/compaction ve tüm managed writer admission'ını uygula; retention policy güncellemeleri append sınırını tüketmeye devam ederken sürdürülebilirlik tamamlanmış sayılmaz.
3. Güvenli delivery/owned restore, protected genel verifier ve owner/TUI kullanıcı yolculuklarını tamamla.
4. GitHub platform paketleri, native kurulum/doctor/ilk görev/güncelleme/geri dönüş, checksums/SBOM ve doğrulanabilir release attestation ekle.
5. Bağımsız preregistered eval ve pilot dahil PRD §27–29 kabulünü kapat. Gerçek API protocol/usage/rate/cancel/charge/model-quality ölçümü sağlayıcı anahtarlarına ayrıca bağlıdır.

Kullanıcı bu turda public dağıtımı da istedi. Release altyapısı mühendislik kabulüne eklenir; kırmızı CI üzerinde kararlı sürüm yayımlanmaz. ChatGPT gerçek login, yerel model/donanım, platform signing identity ve gerçek insan pilotu API anahtarı gerektirmeyen dış kabullerdir; bunlar “yalnız API anahtarları kaldı” şeklinde yeniden etiketlenmez. Tam mühendislik planı [KEYLESS_DELIVERY_PLAN](KEYLESS_DELIVERY_PLAN.md), writer kapsamı [MANAGED_WRITER_INVENTORY](MANAGED_WRITER_INVENTORY.md).
