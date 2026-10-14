# 14 Ekim denetimi: karşılaştırma ve uygulama sırası

Karşılaştırma tabanı `be7cd83b656b63c2a407e87a1a1c2e9f7215f8aa`; ürün otoritesi `prd.md`.
Bağımsız rapordaki öneriler mevcut kaynak ve CI ile kontrol edilir. Kodun varlığı,
fixture başarısı, gerçek provider kabulü ve public paket kabulü ayrı sonuçlardır.

## Kabul edilen, düzeltilen ve kapsamı daraltılan öneriler

| Bulgu | Karar ve gerekçe |
|---|---|
| VBR-101: restore sonrası `MATERIALIZE` reddi | Doğrulandı. Linux/macOS/Docker CI logları ve kota admission aynı hatayı gösteriyor. Dar task/snapshot/hedef sözleşmesiyle ilk düzeltme. |
| VBR-102: üst restore akışı exact retry yapamıyor | Doğrulandı. Alt handoff'un idempotent olması tek başına üst akışı kurtarmıyor. Kalıcı kaynak/hedef intent'iyle reconciliation gerekli. |
| VBR-103: exact-SHA CI ve yayın | Kabul. Kırmızı kaynak engineering/stable paket olarak yayımlanmaz. |
| VBR-104: gerçek ChatGPT görevi | Kabul; API anahtarı istemez. İnsan girişini tamamladı. Yeni paket kabulünden sonra yalnız önceden onaylanan özel görev sürdürülür. |
| VBR-105/106: PRD sınırları ve coverage | Kabul. 128 bölüm + ham amaç + tam PRD = 130 kriter, eski review limiti 128. Ayrıca ilgisiz candidate düzenlemesi sonraki PRD okumalarını engelliyordu. |
| VBR-107: PRD seçimi ve serbest görev | Kabul. PRD seçimi ayrı açık karar olmalı; özel görev metni seçimi kaldırmamalı. |
| VBR-108: TUI | Kabul. Görev/adım/dosya/kontrol/sonraki işlem, terminal escape, ayrı kaynak-plan-doğrulama coverage. |
| VBR-109: coding execution yeniden kurulmalı | Daraltıldı. Mevcut izole candidate → pinned read-only check → output → repair → export döngüsü var. Genel host shell veya canlı proje yazma yetkisi eklemek çözüm değil. Gerçek model döngüsünü kabul etmek gerekiyor. |
| VBR-110: gerçek kalite ve context benchmark | Kabul; offline fixture gerçek model kalite ölçümü değildir. ChatGPT/Ollama ile anahtarsız çalışabilir. |
| VBR-111: proje belleği | Kontrollü source-bound öneri ve kullanıcı kabulü yararlı. Bellek yeni veri/araç izni üretemez; privacy ve silme lineage'ı korunmalı. |
| VBR-112: standart skills | Yararlı ergonomik genişleme. Mevcut signed advisory registry ve MCP effect gate yeniden yazılmaz. Skill metni script çalıştırma izni değildir. |
| VBR-113/114: LLM worker/multi-writer | Gerçek eksiklerle deterministik worker'lar ayrılır. PRD Faz F; ilk kararlı A–D'nin ön koşulu değil. Kullanıcının geniş kapsam isteğinde ayrı, bütçe/lease/merge kabulü gereken iş. |
| `physical_quota_partition_handoff_v1` capability adı | Rapordaki ad güncel kodla uyuşmuyor. Mevcut kalıcı capability `physical_quota_restore_partition_v1`; gereksiz format/ad değişikliği yapılmaz. |
| IDE/Desktop/Web eksikliği | İlk kararlı CLI/TUI sürümünün zorunlu kabulü olarak alınmadı; PRD §2.2 ve kapsam sınırı esas. |
| Rakiplerden daha iyi kalite iddiası | Ölçüm olmadığı için alınmadı. Mimari özellik sayısı rekabet üstünlüğü kanıtı değil. |

## Uygulama ve kabul sırası

1. **Restore çalışma kopyası:** yalnız `artifact/MATERIALIZE/PAYLOAD`, absent exact
   `tasks/<task>/candidates/<digest>`; committed ARCHIVE inode/volume; mevcut immutable
   manifest/schema/task/canonical snapshot digest ve request digest doğrulanır. Artifact
   katmanı blobları doğrular, ilk mkdir öncesinde üst sınır reserve eder, temp/final
   publication ve ölçülmüş commit'i yapar. Başka arşiv alt yolları bu izni kazanmaz.
2. **Restore reconciliation:** backup inode/manifest, authority/ledger ve hedef intent'i
   kalıcı bağlanır. Rename sonrası retry aynı inode/allocation ile settle edilir; yabancı
   hedef benimsenmez. Eksik pathname UNKNOWN kapasiteyi serbest bırakmaz.
3. **PRD ve kullanıcı akışı:** bounded offset parser, 130 review limiti, değişmeyen PRD'nin
   current-candidate okuması, açık PRD seçimi, bounded başlangıç bütçesi ve insan review
   bağlantısı. `REVIEW_REQUIRED` ile yapısal plan bitişi semantik tamlık olarak sunulmaz.
4. **Global kota sınırları:** owner-store bootstrap kaydı ilk privacy mkdir'den önce;
   allocator overhead ve dış metadata yaşam döngüsü ölçülmüş ayrı sınıflar; Docker
   instance sayımı yerine engine kapsamında kalıcı atomik identity admission.
5. **Kullanıcı uzantıları:** source-bound lazy SKILL.md, güvenli memory admission ve
   bounded worker/routing genişlemeleri; her biri kendi kalıcı uyumluluk ve kaynak
   sınırlarıyla. Kullanılmayan taslak API tamamlanmış özellik sayılmaz.
6. **Yayın:** gerekli 16 CI işi exact kaynak için SUCCESS → paket ve attestation →
   anonim asset readback → gerçek Windows launcher/PATH/store kabulü → onaylanmış
   ChatGPT görevi. İlk gerçek görevin UNKNOWN kaydı yalnız onaylanan üst sınırla yerel
   muhasebeleştirilir; ölçülmüş usage veya başarılı yanıt olarak gösterilmez.

Yerelde yeni davranışları hedefleyen küçük negatif/pozitif/crash-retry grupları
çalıştırılır. Hosted full/race/fuzz/migration ve gerçek engine kapıları kaldırılmaz;
aynı geniş test grubu her küçük UI düzenlemesinde tekrar çalıştırılmaz.

## API anahtarı ayrımı

OpenAI ve Anthropic anahtarları gerçek adapter/usage/maliyet/rate-limit/rotation ve
provider kalite kabulü için gereklidir. ChatGPT plan girişi, Ollama model kurulumu,
CI/release/kurulum, privacy/kota, PRD/TUI, pilot ve gerçek benchmark işlerindeki eksikler
API anahtarı gereksinimi olarak sınıflandırılamaz. OS signing/notarization ayrıca signing
kimlikleri ister. Bu belge bütün bu kapıların geçtiğini veya yalnız API işleri kaldığını
iddia etmez; kabul sonuçları exact kaynak ve kanıtla ayrıca kaydedilir.
