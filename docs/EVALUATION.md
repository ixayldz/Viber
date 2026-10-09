# Anahtarsız değerlendirme kullanımı

Viber 0.9.0-dev iki ayrı yerel değerlendirme yüzeyi sunar. İkisi de API anahtarı istemez. Rapor içe aktarma Docker istemez; bağımsız aday testi digest ile sabitlenmiş, önceden indirilmiş Linux Docker image'ı ister. Bunlar üretim release veya gerçek model üstünlüğü kabulü değildir.

## Önceden tanımlı ölçümleri raporlama

Repo'daki örnekler tamamen sentetik muhasebe verisidir. Gerçek benchmark sonucu değildir. İlk tekrarda iki çözüm görevi ve ayrı bir kontrol görevi bulunur; diğer tekrarlar bilerek eksiktir.

~~~powershell
.\bin\viber.exe eval-report `
  --protocol examples/evaluation/protocol.json `
  --observations examples/evaluation/measurements.jsonl `
  --output .cache/my-eval-report --json

.\bin\viber.exe eval-report-inspect --bundle .cache/my-eval-report --json
~~~

Çıktı dizini yeni olmalı ve üst dizin mevcut olmalıdır. Komut private bir protocol.json, observations.json, report.json ve en son manifest.json oluşturur. İkinci komut bütün dosya hash'lerini denetler ve raporu ölçümlerden yeniden hesaplar. Bir rapor değerini ve onun manifest hash'ini birlikte değiştirmek, hesaplama sonucuyla uyuşmayan bir raporu geçerli yapmaz.

Protocol JSON aşağıdakileri deney başlamadan belirler:

- Task/repo kimlikleri, DEVELOPMENT/VALIDATION/HOLDOUT ayrımı, history cutoff ve başlangıç zamanı; aynı repo farklı split'lere atanamaz.
- SOLUTION ve CONTROL kohortları; kontrolün beklenen CLARIFY/SAFE_BLOCK/UNKNOWN/AUTHORIZED_REPAIR davranışı.
- Model/decoding/tool/environment/budget/privacy/safety/adapter/prompt/kernel/dependency/pricing digest'leri ve açık currency. Harness karşılaştırmasında ortak model/tools/environment/budget koşulları eşleşmelidir.
- Tekrar sayısı, sabit primary_repeat, seed, bootstrap örnek sayısı, karşılaştırma listesi ve FIXED_ASSIGNMENT_NO_BEST_RUN stopping rule.
- PAIRED_CONTINUATION için her görevin başlangıç checkpoint digest'i.
- max_latency_ratio_ppm ve max_rework_ratio_ppm. Örneğin 1100000 oranı 1,10 sınırını ifade eder; authoritative JSON ondalık sayı kabul etmez.

Her JSONL satırı protocol digest ve task/arm/repeat'ten türetilen assignment ID'ye bağlıdır. Aynı assignment ikinci kez verilemez. Bir solution koşusu unsupported, timeout, failed veya missing olduğunda ana paydada kalır. Üç denemeden birinin geçmesi görevin tamamını PASS yapmaz. Primary ve bütün tekrarlar ayrı gösterilir; eşit tekrar sayılarıyla task ortalamalarının eşit ağırlıklı aggregate'i kullanılır.

Kernel VERIFIED/strict success beyanları bağımsız evaluator sonucundan ayrı kaydedilir. Başarısız evaluator sonucu false verified/false success sayılır. UNKNOWN evaluator varsa ilgili yanlış beyan oranı kesin bir sayı gibi verilmez; değerlendirilemeyen claim sayısı ayrıca görünür. Kontroller çözüm başarı sayısını artırmaz.

Maliyetlerde iş ve ortak evaluator ayrı tutulur; başarısız retry'nin toplam maliyeti satırda bulunmalıdır. Quantity biçimi `{"known":false,"value":7}` bilinmeyen toplamın en az 7 olduğunu belirtir. Eksik API/yerel compute/index/insan rework gözlemi sıfır kabul edilmez. Cost per independent success paydası sıfırsa veya toplam maliyet eksikse null olur. Muhafazakâr ledger sayıları ölçülmüş para/CPU diye etiketlenmemelidir.

Eşleştirilmiş aralıklar repository cluster bootstrap ve sabit PCG seed kullanır. Karşılaştırma sayısı için Bonferroni kuyruk düzeltmesi vardır. Tek repo için belirsizlik aralığı verilmez; tanımsız ratio örnekleri sessizce atılmaz. Minimum repository cluster sınırı istatistiksel güç garantisi değildir. Wilson aralıkları yalnız tanımlayıcı oran aralıklarıdır; adoption için cluster aralıklarının yerine geçmez. Aralıklar gözlenen repo örneklemine koşulludur ve bütün metric/split/primary/repeated yüzeylerinde tek bir ortak familywise garanti olarak sunulmaz.

İçe aktarılan receipt digest bir kanıt referansıdır; gerçek evaluator'ın çalıştığını doğrulamaz. evidence_mode daima IMPORTED_METADATA_UNATTESTED, release_evidence ve adoption_allowed daima false'tur. metadata_supports_preregistered_statistical_target yalnız verilen metadata üzerinde tanısal bir hesaplamadır; doğrulanmış üstünlük kararı değildir.

## Bir terminal adayın bağımsız gizli STDIO testi

Önce asıl görevi olağan run akışıyla tamamlayın. Public testleri ve raw goal/dependency review'ını [README](../README.md) anlatır. Görev terminal olmalı; bekleyen model/native effect, token/resource reservation veya kullanıcı input barrier bulunmamalıdır.

Background owner aynı store kilidini tutuyorsa terminal görevden sonra owner-stop --store PATH ile ownerı durdurun; evaluator original owner kilidini doğrudan alır.

Gizli test için plan/runtime biçiminde ayrı bir operator recipe hazırlayın. Bu dosya asıl kaynak kökün ve asıl store'un dışında kalmalıdır. Check kind TEST, requirement_ids ["user-goal"], sabit argv ve gerçek dependency closure; runtime pinned profile ve V4 KERNEL_STDIO_EXACT_V1 suite taşır. Case input/expected_stdout/expected_stderr byte'ları JSON'da base64'tür. Beklenen çıktılar ham kullanıcı hedefiyle tutarlı olmalıdır. Suite bütün seçili case'leri 2..4 kez çalıştırır; genel semantik kapsam iddiası vermez.

~~~powershell
.\bin\viber.exe check-prepare --file C:\eval\hidden-recipe.json --output C:\eval\hidden-bound.json

.\bin\viber.exe eval-candidate my-task --store C:\tasks\original `
  --recipe C:\eval\hidden-bound.json --output C:\eval\independent-run --json

.\bin\viber.exe eval-inspect --bundle C:\eval\independent-run --json
~~~

Yeni evaluator dizini orijinal root/store ile kesişemez. Asıl owner kilidi tutulurken mevcut immutable CAS candidate import edilir. Bugünkü live klasör ikinci kez okunmaz; orijinal file mode, Git metadata, directory ve snapshot identity korunur. Eval owner kendi task-scope arşivinde aynı capture'ı tutar. Gizli oracle, protected recipe, çıktılar ve check receipts yalnız bu ayrı owner alanındadır. Frozen source metadatası original journal ile bütün olarak karşılaştırılır ve dispatch öncesinde evaluatorın kalıcı ham girdisindeki source digestine bağlanır. Bu yeni private store ayrıca adayın veri kopyasını tutar; yedekleme ve saklama kapsamınızı bu dizinle birlikte yönetin.

Evaluator asıl görev modelini çağırmaz. Sabit bir yerel fixture yalnız kayıtlı check ID'lerini sırayla bir kez seçer; repair, candidate değişikliği veya en iyi aday seçimi yapmaz. Her subject yeni scratch ile normal native resource admission, durable lease, generation/deadline ve orphan recovery yolunu kullanır. Baseline ve current grupları bu evaluator'da aynı dondurulmuş adaydır; baseline tekrarları ek tutarlılık/maliyet overhead'idir ve kayıttan çıkarılmaz. Gruplar/repeat'ler uyuşmuyorsa PASS verilmez.

Original quality/fulfillment/claim ve bağımsız verdict ayrı gösterilir. Gerçek kabul testinde uppercase hedefi için yalnız uppercase public input'tan geçen cat uygulaması VERIFIED alırken lowercase hidden input'ta FAIL verir; false_verified ve false_success yalnız evaluator sonucunda kaydedilir. Asıl task state, kaynak dosyası ve model mesajları değişmez. eval-inspect sonucu ayrı owner'ın doğrulanmış retained receipts'inden yeniden üretir; JSON PASS veya güncellenmiş manifest hash'i yetmez.

| Kod | eval-candidate / eval-inspect |
|---|---|
| 0 | Seçili bağımsız exact STDIO koşuları PASS |
| 1 | Tam, native-owned ve quiescent test kaydı FAIL |
| 2 | Eksik/flaky/UNKNOWN bağımsız sonuç |
| 4 | Argüman, integrity, admission veya altyapı hatası |

eval-report ve eval-report-inspect için 0 yalnız idari raporlama başarısıdır.

Çalışma yarıda kalırsa evaluator owner korunur: C:\eval\independent-run\owner ve task ID independent. Önce runtime-info independent --store ... ile riski inceleyin. Daha yeni generation altında explicit reconcile-native-risk ile önceki owned subjects fence edilebilir. Kayıp test çıktısı geri kazanılmış sayılmaz; kör retry veya PASS üretilmez. Manifest yayımlanmamış bir çalışma bitmiş rapor değildir.

Bu yüzey post-hoc frozen candidate validation'dır. Protocol dosyasının sonradan yazılması deneyi preregistered yapmaz. Tam preregistered dataset runner, original model koşullarının runtime attestation'ı, gerçek paired continuation trials, localization labels/reference p95, bütün V0–V5 framework ve insan/power/pilot kabulü bu iki yüzeyin sonucu olarak kapanmaz. Native result'un adoption_allowed/release_evidence alanları false; geniş safety verdict UNKNOWN kalır. Yerel compute parasal maliyeti ve insan rework'i fixture token sıfırından çıkarılmaz.

[Güven zinciri](adr/0015-independent-evaluation.md) · [Gerçek doğrulama kaydı](VALIDATION.md)
