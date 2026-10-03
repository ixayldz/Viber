# Harness — Uygulama ve değerlendirme planı

Tarih: 22 Eylül 2026. Bu plan [URUN_MIMARISI_V2.md](C:/Users/ixayl/Desktop/harrnes/URUN_MIMARISI_V2.md) tasarımını küçük, ölçülebilir teslimlere ayırır. Süre tahmini değildir: ekip kapasitesi, mevcut kod ve hedef platformlar bilinmediğinden fazlar takvimle değil çıkış koşullarıyla tanımlanmıştır.

## 1. Karar disiplini

Her mimari değişiklik kısa bir ADR ile kaydedilir: problem, seçilen davranış, alternatif, garanti sınırı, test ve geri dönüş koşulu. Mevcut v2 önerisi için başlangıç kararları:

| ADR | Karar | Kabul nedeni | Yeniden açma koşulu |
|---|---|---|---|
| 001 | Kendi agent loop'u; doğrudan model adapter'ı | Araç/state kontrolünün çekirdekte kalması | Harici CLI'nin aynı kontrol sözleşmesini sunması |
| 002 | Yerel tek writer kernel + SQLite + blob store | İlk ürün için recovery ve operasyonel sadelik | Ölçülmüş gerçek çok host ihtiyacı |
| 003 | İzole candidate; journal ile apply | Kullanıcı değişikliklerini koruma | Backend'in daha güçlü snapshot/transaction primitive'i |
| 004 | Observation/Claim/Decision ayrımı | Kaynak ile yorumun otoritesini ayırma | Daha basit model aynı hata sınıflarını engellerse |
| 005 | Checkpoint typed state'ten üretilir | Compaction'a authoritative state'i yeniden yazdırmama | İnvariant korunarak daha ucuz yol bulunursa |
| 006 | Tek model, tek writer başlangıcı | Harness katkısını ve maliyeti ayırabilme | Ablation'da fayda gösteren router/paralellik |
| 007 | Model/harness/policy versiyonlu eval | Tekrarlanabilir karşılaştırma | Benchmark protokolü revizyonu |
| 008 | Windows istemcisi ve execution backend ayrı | Platformlar arasında sahte güvenlik eşdeğerliği kurmama | Native backend eşdeğer testleri geçerse |

## 2. Faz A — Riskli varsayımları küçük spike'larla çöz

Üç teknik belirsizlik önce çözülür:

1. Dirty Git workspace'i kullanıcı dosyasını kaybetmeden candidate'a taşıma ve geri uygulama.
2. Process tree'nin gerçek FS/ağ sınırları; Windows istemcisi ile seçilen execution backend'in dosya/izin davranışı.
3. İki farklı model API protokolü ve bir yerel endpoint'te aynı canonical tool-loop sözleşmesi.

Çıktı: küçük çalışan spike'lar, backend capability matrisi, ADR revizyonu ve seçilmiş implementasyon dili. Başarısız backend'e daha zayıf mod etiketi verilir veya destek ertelenir. Bu fazda graph, embedding, büyük TUI ve training sistemi geliştirilmez.

Çıkış: stale patch reddi, kullanıcı editinin korunması, subprocess sınırının doğrulanması ve provider tool-call/result zincirinin çalışması gösterilmiş olmalı. Sandbox problemini prompt ile kapatan spike kabul edilmez.

## 3. Faz B — Tek agent ile tam dikey akış

Teslim:

- `run`, `status`, `diff`, `pause`, `resume`, `cancel`, `inspect`.
- TaskSpec v1, input journal ve sürümlü event envelope.
- ModelAdapter, lexical search, read receipt, patch, sandbox process.
- Baseline/candidate snapshot, operation intent/receipt, resource reservation.
- Kriter bazlı doğrulama ve JSONL final rapor.

Hedef demo: orta boy bir değişikliği yaparken süreci öldür; arada kullanıcı dosya değiştirsin; resume et; eski patch'i reddet; yeniden doğrulanan diff üret.

Çıkış koşulları:

1. Kritik yerel mutation sınırlarının her birinde fault injection sonrası tutarlı recovery.
2. Kullanıcının mevcut ve sonradan eklediği değişiklikler korunuyor.
3. Eski snapshot testiyle yeni snapshot `VERIFIED` olamıyor.
4. İşlem tekrar güvenliği sınıfına göre recovery çalışıyor.
5. Model akışı güvenlik/policy testini geçersiz kılamıyor.

“Bütün görevleri çözüyor” şart değil; başarısızlığını doğru ve kurtarılabilir biçimde raporlaması şart.

## 4. Faz C — Süreklilik ve context kalitesi

Teslim:

- Context manifest ve inclusion reason.
- Stable prefix, yakın tool geçmişi, typed checkpoint, bounded compaction.
- Observation/Claim applicability ve incremental invalidation.
- Context/history paging, `explain`, token/usage raporu.
- Model switch ve context overflow recovery.

Karşılaştırma: aynı modelin basit transcript + bounded summary baseline'ına karşı, aynı task ve bütçede bu katmanların katkısı.

Çıkış: uzun görev ve resume kohortunda yanlış constraint kaybı veya stale evidence kullanımı azalmalı; toplam doğrulanmış başarı ve maliyet Pareto dengesi iyileşmeli. Sadece prompt token sayısının düşmesi yeterli değil. Kazanç çıkmazsa daha küçük state/context modeli korunur.

## 5. Faz D — Günlük kullanım ve beta

Teslim:

- Sade TUI, steering, on-demand detach/attach ve aynı kernel'i kullanan headless akış.
- Onboarding/doctor, environment profilleri, export/retention ve log erişim kontrolleri.
- İki remote provider adapter'ı ve bir local endpoint için conformance paketi.
- TypeScript/JavaScript ve Python için iyi dosya outline/symbol deneyimi; diğer dillerde fallback.
- Apply/restore için conflict raporu; desteklenen platform matrisi.

Pilot: küçük bir geliştirici grubunda gerçek iş akışı. Önerilen başlangıç 5–10 kullanıcı; bu sayı istatistiksel başarı garantisi değil, kurulum ve etkileşim sorunlarını erken bulma ölçeğidir. Kod dışarı çıkarmadan lokal ölçüm/isteğe bağlı raporla ilerlenebilir.

Ölç: ilk faydalı değişikliğe süre, kurulumdan ilk göreve geçiş, inceleme/düzeltme süresi, gereksiz onay, resume güveni, tekrar kullanım. “Beğendin mi?” tek başına ürün doğrulaması değildir.

## 6. Faz E — Repository intelligence deneyleri

Sıra: exact/lexical → symbol → resolved graph → history → semantic retrieval → reranker. Bir defada bütün katmanlar eklenmez.

Her katman için:

- Sabit candidate snapshot ve held-out görevler.
- İlk arama latency'si, index cold start ve disk/memory maliyeti.
- Localization recall@k ve gerçekten çözülen görev oranı.
- Stale-index ve unsupported-language koşullarında fallback.
- Toplam model + indexing + maintenance maliyeti.

Graph test seçiminde kullanılıyorsa missed-impact oranı da ölçülür. Geriye dönük test coverage eksikliği, “bu testi atlayabiliriz” için kesin kanıt değildir.

Çıkış: hedef kohortta ölçülmüş kazanım; kısa görevlerde kabul edilemez latency/kurulum regresyonu olmaması. Kazanç yalnız dar bir görev sınıfındaysa özellik o sınıfta etkinleşir.

## 7. Faz F — Kontrollü paralellik ve model routing

Ön koşul: tek agent baseline'ı güvenilir ve metrikleri sabitlenmiş.

Önce salt okuma worker'ları; sonra isolated writer candidate'ları, lease generation ve integration queue. Model routing görev boyunca sticky seçimle başlar, gerekirse phase-level seçim eklenir.

Deney sorusu: ek model çağrısı ve birleştirme maliyeti dahil, aynı doğruluk seviyesinde süre/maliyet iyileşiyor mu? Reviewer'ın farklı model olması bağımsızlık kanıtı değildir; yakalanan gerçek hata ve yanlış pozitifler ölçülür.

Çıkış: single-agent karşısında belirlenmiş kohortta fayda; conflict, budget overshoot ve yanlış final doğrulama artışı olmaması. Sonuç kötüleşirse multi-agent varsayılan kapanır. Dağıtık worker, recursive delegation ve global planner eklemek zorunlu ilerleme sayılmaz.

## 8. Faz G — Öğrenme ve ekip özellikleri

Yalnız önceki fazlarda yeterli veri ve ihtiyaç çıkarsa: versioned skills, offline ranker/router optimizasyonu, ekip policy yönetimi, remote execution. Fine-tuning/RL ayrı veri/yetki ve maliyet değerlendirmesi ister.

Training export, telemetry export'tan bağımsız açık tercihtir. Kullanıcı kabulü veya test geçişi otomatik “altın etiket” olmaz. Yeni policy shadow/canary değerlendirme ve rollback mekanizmasıyla çıkar; güvenlik çekirdeği online öğrenmeyle değişmez.

## 9. Benchmark protokolü

### 9.1 İki farklı karşılaştırma

**Harness etkisi:** aynı model sürümü, aynı reasoning/decoding ayarı, aynı tool yeteneği, aynı environment ve bütçe; sadece harness bileşeni değişir. Baseline: iyi tasarlanmış basit tool loop + transcript + bounded summary + aynı doğrulama araçları. Yapay olarak zayıf baseline seçilmez.

**Ürün etkisi:** rakip ürünler kendi önerilen ayarlarında, eşit görevler ve raporlanmış toplam bütçeyle. Tam model eşitliği mümkün değilse sonuç ürün sistemi karşılaştırması olarak etiketlenir; harness'e nedensel başarı payı yazılmaz.

### 9.2 Veri kümeleri

Başlangıç için önerilen 120 görevlik engineering set:

| Kohort | Görev sayısı | Amaç |
|---|---:|---|
| Kısa ve açık değişiklik | 30 | Yeni altyapının basit işleri yavaşlatmaması |
| Çok dosyalı bug/feature | 30 | Context seçimi ve doğrulama |
| Migration/refactor | 20 | Bağımlılık, API uyumu, regression |
| Uzun görev + compaction/resume | 20 | Süreklilik ve state kaybı |
| Eksik test / bozuk ortam / belirsiz istek | 20 | Dürüst completion ve blocker davranışı |

Bu sayı başlangıç tasarımıdır, istatistiksel yeterlilik iddiası değildir. Repo'lar geliştirme ve holdout arasında ayrılır; mümkünse zaman ayrımı da uygulanır. En az iki desteklenen dil ve farklı repo boyutları dahil edilir. Public benchmark'lar ek karşılaştırma sağlar; özel runtime failure set'inin yerini tutmaz.

History/retrieval verisi görev başlangıç commit'i ve tarihinden sonrasını içermez. Çözüm patch'i, sonraki issue yorumları ve eval etiketleri agent'a görünmez. Aynı görevin tekrarları arasında candidate ve memory sıfırlanır; cross-task learning ayrı deneydir.

### 9.3 Çalıştırma ve rapor

1. Önce geliştirme setinde ayarlar seçilir; holdout görüldükten sonra onun için ayar yapılmaz.
2. Başlangıç taraması bir run/task; karar belirleyici kohortlar için önceden seçilmiş en az üç tekrar. Bütün tekrarların sonucu raporlanır; en iyi run seçilmez.
3. Model, provider, endpoint, adapter, prompt, kernel, tool, dependency, image ve dataset sürümleri manifest'e yazılır.
4. Token, dolar/para birimi, yerel compute, süre ve retry limitleri ayrı kaydedilir. Pricing sürümü rapora bağlanır; sabit eski fiyat varsayılmaz.
5. PASS/FAIL/UNKNOWN/WAIVED ve environment setup hataları ayrı sayılır; başarısız görevler maliyet hesabından çıkarılmaz.
6. Eşleştirilmiş farklar, belirsizlik aralıkları ve repository bazında sonuçlar raporlanır. Aynı repo görevleri bağımsızmış gibi aşırı dar güven aralığı üretilmez.
7. Küçük veri setinde sonuç belirsizse “üstünlük kanıtlanmadı” yazılır. Yeni özellik release gerekçesi olarak sadece tek başarılı demo kullanılmaz.

### 9.4 Metrik tanımları

```text
Verified Success Rate = strict verified tasks / all assigned tasks

Cost per Verified Task = all run costs including failures / strict verified tasks

False Verified Rate = runs declared VERIFIED but failing independent checks
                      / all runs declared VERIFIED

Recovery Success = injected interruption cases resumed consistently
                   / all applicable injected cases

Stale Admission Rate = operations admitted with known-invalid required bindings
                       / all checked consequential operations

Human Rework = measured review/correction time until accepted result
```

Sıfır çözülen görevde cost-per-verified tanımsızdır; sıfır maliyet gösterilmez. False Verified Rate'te sıfır gözlem sıfır gerçek risk demek değildir; payda ve belirsizlik aralığı yazılır. Watchdog'daki edit/evidence sayısı north-star değildir.

Quality, latency ve cost tek ağırlıklı skora erkenden sıkıştırılmaz. Pareto karşılaştırması ve kullanıcı hedefi kullanılır. Onay sayısı azaltılırken yetki ihlali artıyorsa iyileşme sayılmaz.

### 9.5 Ablation sırası

```text
B0: basit ve güçlü tool loop baseline
B1: B0 + typed task/checkpoint state
B2: B1 + snapshot-bound observations/verification
B3: B2 + context compiler/compaction
B4: B3 + symbol/graph (ayrı deneyler)
B5: B3/B4 + history/semantic/reranker (ayrı deneyler)
B6: en iyi tek-agent yapı + bounded multi-agent
B7: en iyi yapı + model routing
```

Bu sıra deney kontrolüdür; güvenlik sınırları test için gevşetilmez. Bileşenler arasında etkileşim çıkarsa seçilmiş kombinasyonlar ayrıca denenir. “Bütün özellikler açık” sistemin neden iyi/kötü olduğunu tek başına açıklamaz.

## 10. Runtime için zorunlu adversarial senaryolar

| Senaryo | Beklenen davranış |
|---|---|
| İlk dosya yazıldıktan sonra crash | Yarım operation tanınır; yanlış publish yok |
| API yan etkisi oldu, response kayboldu | Kör retry yok; idempotency/reconciliation veya UNKNOWN_OUTCOME |
| Test PASS, ardından kaynak editlendi | Eski receipt yeni candidate'ı doğrulayamaz |
| Lockfile değişti, source file aynı | Bağımlı davranış kanıtı yeniden değerlendirilmeli |
| Agent read sonrası kullanıcı edit yaptı | Stale proposal conflict/re-read; kullanıcı editine overwrite yok |
| Kullanıcı steering gönderirken işlem queued | Eski epoch'la dispatch/publish reddedilir |
| Lease süresi doldu, eski worker döndü | Eski fencing token ile publish yapılamaz |
| İki çağrı aynı son bütçeyi istiyor | Atomik rezervasyon; yalnız uygun toplam kabul edilir |
| Worktree'den sibling dizine kaçış | OS sınırı engeller; path string kontrolüne bağımlı kalmaz |
| Junction/symlink/case alias kaçışı | İzin gerçek hedefte uygulanır |
| Test script'i ağ veya dış dosya erişimi deniyor | Process tree sınırı uygulanır |
| Test çıktısı sahte PASS metni basıyor | Runner/check tanımı olmadan PASS receipt oluşmaz |
| Test/acceptance listesi agent tarafından azaltılıyor | Spec değişikliği olarak ele alınır; sessiz completion yok |
| Bozuk index / kaçırılmış watcher event'i | Kaynağı revalidate et; direct-read fallback |
| Tool-call JSON stream yarıda kesildi | Eksik çağrı yürütülmez; attempt kaydı korunur |
| Context overflow | Çağrı öncesi budget; zorunlu kuralları sessiz kesme yok |
| Compaction kullanıcı yasağını atlıyor | Typed constraint korunur, summary otorite kazanamaz |
| Remote fallback privacy kısıtına aykırı | Çağrı reddedilir; izinli alternatif veya block |
| Plugin kernel DB'sine doğrudan yazmak istiyor | İzole process erişimi reddedilir |
| Disk dolu / bozuk blob / schema migration yarım | Tutarsız checkpoint ile devam edilmez; recoverable hata |
| Daemon iki kez başlıyor | Tek writer sahipliği ve generation korunur |
| Restore sırasında kullanıcı yeni edit yaptı | Üç yönlü conflict; geniş reset yok |
| JSONL istemcisi kopuyor veya yavaşlıyor | Kernel çalışmaya devam; cursor ile reconnect |
| Telemetry kapalı | Model için izinli egress dışında telemetry/training gönderimi yok |

Fault injection testleri başarı oranı testlerinden ayrı raporlanır. Kritik güvenlik/koruma senaryolarında test seti için geçiş oranı %100 release koşuludur; bu bütün olası saldırıların çözüldüğü anlamına gelmez.

## 11. Başlangıç performans hedefleri

Aşağıdakiler ölçülmüş değer değil, ilk ürün deneylerini yönlendirecek önerilen hedeflerdir. Referans makine, repo boyutu ve sıcak/soğuk cache durumu belirlenmeden pazarlama vaadi olarak kullanılmaz.

- Ağ/model beklemesi hariç, sıcak durumda ilk CLI durumunu gösterme p95 < 1 saniye.
- Sıcak metadata ile tipik context derleme p95 < 250 ms; kapsam/tokencount yüküne göre ayrı rapor.
- Explicit küçük değişiklikte zorunlu tam-repo indexing beklemesi olmaması.
- Büyük log akışında model context'i ve UI belleğinin bounded kalması; artifact quota uygulanması.
- Pause/steering event'inin dayanıklı kaydı sonrası yeni eski-epoch action kabul edilmemesi. Çalışan process'in bitiş süresi ayrı metrik.

Bu hedefler tutmuyorsa önce hot path sadeleştirilir. Retrieval kalitesini artırmak için her çağrıya planner/reranker/reviewer eklemek varsayılan çözüm değildir.

## 12. Release ve vazgeçme koşulları

Her faz için zorunlu koruma testleri geçmeden release olmaz. Model performansında ise bütün görev türlerinde kazanım şartı konmaz; özellik hedeflediği kohortta değerlidir ve diğerlerinde kapalı kalabilir.

Yeni bir optimizasyonun varsayılan olması için geliştirme setinde önceden belirlenmiş minimum fayda ve kabul edilebilir regresyon sınırları holdout'ta karşılanmalı. Örnek başlangıç kararı: aynı bütçede en az 5 yüzde puan verified-success artışı veya başarıda en fazla 2 puan kayıpla toplam maliyette en az %20 azalma. Bu sayılar proje hedefidir, literatür sonucu değildir; veri belirsizliği bu ayrımı göstermiyorsa özellik deneysel kalır.

Kesin durdurma koşulları: kullanıcı dosyası kaybı, eski yetkiyle yeni işlem, policy bypass, farklı snapshot'a ait receipt ile VERIFIED, belirsiz dış etkinin kör tekrarı. Bunlar daha iyi benchmark puanı karşılığında kabul edilmez.

Faz B–D sonunda bu ürün günlük işte mevcut iyi bir baseline'dan anlamlı fayda göstermiyorsa çözüm daha çok katman eklemek değildir. Hedef iş/kullanıcı, state overhead'i ve araç ergonomisi yeniden değerlendirilir. Geniş vizyonu taşıyacak ürün kanıtı bu aşamada kazanılmalıdır.

## 13. Sonraki somut mühendislik işi

İlk implementasyon paketi: ADR 001–008'i kod sözleşmelerine dönüştür; `TaskSpec`, `Operation`, `WorkspaceSnapshot`, `VerificationReceipt` schema'larını kur; script'li sahte model ve fault-injectable runner ile tek bir görevi baştan sona yürüt. Ardından gerçek model adapter'ını ekle.

İlk başarı gösterisi, en iyi prompt veya en büyük graph değildir: kullanıcı dosyası değişmiş ve süreç çökmüş olsa bile, sistemin doğru candidate'ı doğru test kanıtıyla teslim etmesidir.
