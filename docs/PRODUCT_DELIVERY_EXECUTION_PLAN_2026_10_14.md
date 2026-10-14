# Viber kullanıcı akışı ve teslim planı — 14 Ekim 2026

Hedef: kullanıcı doğrulanmış paketi kursun, terminalde `viber` yazsın, ChatGPT hesabıyla giriş yapsın, proje/PRD seçsin, planı ve önerileri slash komutlarıyla incelesin, kayıtlı kontrolleri çalıştırsın ve sonucu dışarı alsın. `prd.md` yetkilidir. Plan, gerçek kullanıcı kabulünü veya production durumunu kod varlığından türetmez.

## 1. Bu değişiklikte uygulanan kaynak işleri

| İş | Uygulama | Gösterdiği ve göstermediği şey |
|---|---|---|
| Kalıcı PRD sözleşmesi | `TaskSpec.PRD`: exact capture/path/hash/size/input; ham PRD arşivde; source-span section kriterleri | Kaynak sabittir; section çıkarımı semantik gereksinim yorumu değildir |
| Gerçek PRD okuma coverage'ı | Başarılı `fs_read` sayfaları birleştirilir; candidate/digest/byte aralıkları kalıcıdır | Modelin “okudum” yazması yeterli değildir; bütün byte aralığı okunmalıdır |
| PRD plan gate'i | Complete read → source-bound `plan_propose` → required kriterleri kapsayan typed DAG → candidate writes | Plan/progress kalite veya bağımsız check PASS üretmez |
| Yeniden açma ve yeni attempt | Manifest/spec/coverage doğrulaması; yeni attempt aynı ham PRD'yi koruyorsa yeni baseline'a bağlanır | Farklı PRD bytes'ı sessizce eski intent'in yerine geçmez |
| Kolay onay | `/requests`, `/approve [ID]`, `/reject [ID]`, explicit `/resume` | Tek pending request varsa ID atlanabilir; birden fazla istekte exact seçim gerekir |
| Okunabilir inceleme | `/status` sonraki adımı gösterir; `/plan` okuma ve coverage'ı; `/diff` bounded patch'i gösterir | Kırpılan/binary değişiklikler açıkça belirtilir; tam dosyalar export'tadır |
| Canlı arayüz | Yeni öneri bir kere görünür; bounded provider delta sayfaları invocation/gap/resync ile gösterilir | Ephemeral model çıktısı untrusted'dır ve check evidence değildir |
| Teslim yolu | `/preview` ve `/export` dizin verilmezse kayıt klasörünün yanında yeni external dizin seçer | Owner lock ve source/store disjoint kontrolleri korunur; kaynak projeye otomatik yazmaz |
| Sihirbazda check bağlantısı | `viber start --prd FILE --check-config ABSOLUTE_FILE` | Yalnız operatorün seçtiği proje/store dışındaki pinned JSON kullanılır; repo script'i yetki kaynağı olmaz |
| Restore kota admission | Backup'ın held-inode SQLite pin'i okunur; sealed shared ledger limitleri kullanılır; staging öncesi reserve/prepare/begin | Kota eksikliği ilk kopyadan önce reddedilir; yeni ledger veya default limit mint edilmez |
| Restore typed settlement | Bound sibling rename; aynı transaction'da owner static/SQLite/control/archive/GC command receipts | Enclosing WRITING durumunda store/archive yeniden import edilmez; final reopen typed üyeleri kullanır |
| Paket uyumluluğu | PRD task contract ve restore partition capability beyanları/registered store taraması | Bu protokollere ihtiyaç duyan store için eski paket seçimi engellenir |
| Kullanıcı README'si | İndirme → launcher → `viber` → ChatGPT → PRD → slash review → export | Public paket ile daha yeni main davranışı ayrılır |

Markdown section çıkarımı bounded'dır: 128 section sınırı, UTF-8 veya temsil edilemeyen içerik `REVIEW_REQUIRED` olarak görünür. Bütün ham PRD korunur. Bu iş bağımsız semantik requirement extraction ve insan coverage review kabulünün yerini almaz.

## 2. Bu commit'in kabul ve yayın sırası

1. Go 1.27.2 ile binary derlemesi ve değişen paketlerin `go vet` kontrolü.
2. Yalnız ilgili dosyalarla main commit/push. Kullanıcının altı mevcut dosya değişikliği korunur.
3. Mevcut hosted CI: bütün native/full/race/fuzz/migration/Docker/vault/package/install/provenance işleri. Yeni kaynak için önceki CI başarısı kullanılmaz.
4. Exact kaynak için 16 job başarılı olduğunda engineering tag/publisher; üç native arşiv, checksums, SBOM ve installation evidence.
5. Anonim public readback: bütün asset byte/hash ve OIDC subject'leri; aynı kaynak SHA'sı.
6. Actual Windows managed upgrade/launcher/PATH/doctor/status; açık store/projection korunumu.
7. Önceden onaylanmış tek özel ChatGPT görevi: eski UNKNOWN rezervasyonu yalnız kabul edilen üst sınırla yerel muhasebeleştirme, düzeltilmiş araç şemasıyla aynı bounded task'ın bir kez sürdürülmesi. Yeni model isteği veya yeni bütçe otomatik yaratılmaz.
8. Gerçek candidate diff ve source korunumu ölçülür. Bağımsız check olmayan sonuç UNVERIFIED kalır.

Kullanıcının hız talebi doğrultusunda bu tur yeni test yazılmadı ve yerel full test/race/fuzz tekrarlanmadı. Hosted CI kapıları kaldırılmadı. Derleme/static kontrol, restore crash/fencing kabulünü tek başına kapatmaz. Publisher kırmızı veya henüz bitmemiş CI üzerinden yeni paket yayınlamaz.

## 3. Ardından genişletilecek PRD işleri

Temel ürün yolu kabul edildikten sonra genişletmeler ayrı source/acceptance değişiklikleriyle yapılır:

1. **Semantik PRD coverage:** ham source span'lerden çıkarılan işlevsel kriterlerin kullanıcıya review edilmesi, versioned spec; bounded bölüm etiketleriyle “gereksinimler tamamlandı” iddiası kurulmaz.
2. **Check kurulum deneyimi:** Go/JS/Python proje discovery'sinden advisory öneri, operator image/command onayı ve protected closure; desteklenmeyen ortam UNKNOWN olarak görünür.
3. **Proje belleği:** aynı proje cross-task kayıtlarının otomatik context admission'ı, güncellik/conflict/policy filtreleri ve her prompt kopyasının deletion lineage'ı birlikte ele alınır.
4. **Standart skills:** SKILL.md yükleme, kaynak digest/version/byte sınırları; skill içeriği egress veya yazma yetkisi veremez. Mevcut signed advisory registry korunur.
5. **Gerçek effectiveness/cohort:** preregistered actual coding görevleri, bütün attempt/UNKNOWN maliyetleri, hidden exact-candidate check ve ölçülen kalite/p95. Sentetik fixture sonuçları gerçek benchmark değildir.
6. **LLM read-only worker:** parent budget reservation, TTL/generation/lease, immutable source-bound output ve cancellation; sabit yerel analyzers LLM worker diye gösterilmez.
7. **Multi-writer:** ancak lease/mutex/isolation/fencing/merge/reverification kabulü sonrasında açılır. Ajanların eşzamanlı canlı projeye yazması varsayılan olmaz.

Bu işler API anahtarı gerektirmeyen implementasyonlardır; ilgili acceptance veya kullanıcı/ortam onayı yine gerekebilir. Hepsini API anahtarı engeline bağlamak doğru değildir.

## 4. Gerçek dış kaynak gerektiren kabul

| Kabul | Gereken |
|---|---|
| ChatGPT ilk coding görevi / cohort | Uygun hesap, plan kullanım izni ve internet; model API anahtarı gerekmez |
| Ollama gerçek model kabulü | Kurulu model ve çalışan yerel endpoint; model API anahtarı gerekmez |
| OpenAI / Anthropic gerçek adapter ve maliyet kabulü | Sağlayıcı API anahtarları ve izinli hesap |
| OS imzası/notarization | İlgili işletim sistemi signing kimlikleri |
| Bağımsız pilot / reference hardware | Gerçek katılımcı/ortam ve ölçüm; API anahtarıyla eş anlamlı değildir |

Stable release, PRD'deki ilgili güvenlik ve kalite kapılarının actual evidence'ıyla açılır. Bu plan engineering preview'i koşulsuz production-ready olarak yeniden etiketlemez.
