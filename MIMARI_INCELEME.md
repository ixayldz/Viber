# Harness: araştırma ve mimari incelemesi

Tarih: 22 Eylül 2026. İncelenen girdiler: `analiz1.md` (156 satır), `analiz2.md` (136 satır), `idea.md` (3.202 satır, 90 bölüm). Bu belge mevcut fikrin eleştirisidir; önerilen tasarım [URUN_MIMARISI_V2.md](C:/Users/ixayl/Desktop/harrnes/URUN_MIMARISI_V2.md), uygulama sırası ve deneyler [UYGULAMA_VE_EVAL_PLANI.md](C:/Users/ixayl/Desktop/harrnes/UYGULAMA_VE_EVAL_PLANI.md) içindedir.

## 1. Hüküm

Ana tez doğru yönde: modeli değiştirilebilir tutmak, görevi konuşma geçmişinden ayırmak, kaynakları sürümlemek ve tamamlanmayı kanıta bağlamak. Fakat mevcut metin uygulanabilir bir sistem sözleşmesi olmaktan çok bileşen kataloğu. En büyük eksik yeni bir araştırma alanı değil; bileşenler çeliştiğinde hangi otoritenin karar verdiği ve garantilerin nerede sona erdiği.

Üç düzeltme belirleyici:

1. Runtime'ın merkezine görev, yetki ve çalışma alanı tutarlılığını koy. Context MMU bunun üzerinde çalışan bir optimizasyon katmanı olsun.
2. Kanıtın kaynağını, güncelliğini, doğruluğunu ve yetkisini birbirinden ayır. Aynı SHA, doğru yorum demek değildir.
3. Uzun vadeli vizyonu koru; ilk ürünü dar bir kullanım alanında ölçülebilir üstünlük gösterecek şekilde çıkar.

“Dünyanın en iyisi” şu anda doğrulanmış sonuç değil, hedef. Belirli görevler, modeller, bütçeler ve ortamlar üzerindeki karşılaştırmalar bu iddiayı destekleyebilir; özelliklerin toplamı destekleyemez.

## 2. Araştırma denetiminin kapsamı

Birincil yayın sayfaları, makale özetleri, resmi proje README'leri ve ilgili resmi teknik belgeler kontrol edildi. Bütün makalelerin deneyleri yeniden üretilmedi; bütün rakiplerin kaynak kodları denetlenmedi. Aşağıdaki “doğrulandı” ifadeleri kaynak ve iddia kapsamının doğrulanmasıdır, bağımsız replikasyon değildir.

### 2.1 Somut düzeltmeler

| Girdi / iddia | Bulgular | Mimariye etkisi |
|---|---|---|
| `analiz2.md:16`: MCP Google tarafından önerildi | Yanlış. Anthropic'in 25 Kasım 2024 duyurusu MCP'nin Anthropic'te yaratıldığını açıkça belirtiyor. [Birincil kaynak](https://www.anthropic.com/news/model-context-protocol). | MCP'yi bağlam genişletme veya cache mekanizması gibi sınıflandırma; entegrasyon protokolü olarak ele al. |
| `analiz2.md:13`: Llama 2 70B 128K | Standart model için yanlış. Meta'nın model kartında 70B için 4K yer alıyor. Özel bir türev kastediliyorsa kimliği verilmemiş. [Model kartı](https://huggingface.co/meta-llama/Llama-2-70b-hf). | Model özellikleri aile adından çıkarılmamalı; tam model kimliği, endpoint ve profil sürümü kullanılmalı. |
| `analiz2.md:63`: LongBench 4.7K'ya kadar | Yayının özetindeki kapsamla uyuşmuyor: İngilizce ortalama 6.711 kelime, Çince ortalama 13.386 karakter. Bunlar token maksimumu değildir. [LongBench](https://arxiv.org/abs/2308.14508). | Kelime, karakter, token; ortalama ve maksimum birbirine karıştırılmamalı. |
| `analiz2.md:50`: Ralph esasen alt ajanların halüsinasyon denetimi | Tekniğin temelini yanlış daraltıyor. Yazarın anlatımı döngüsel, görev odaklı yürütmeyi vurguluyor; alt ajanlar bunun bir parçası olabilir. [Yazarın açıklaması](https://ghuntley.com/ralph/). | Ralph bir doğruluk kanıtı veya zorunlu hakem mimarisi olarak alınmamalı. |
| `analiz2.md:55`: cache varsa sabit içerik yeniden gönderilmemeli | Genelleme yanlış. Claude prompt caching, önbellek isabeti için aynı prompt segmentlerini gerektiriyor; cache ile kalıcı server-side konuşma aynı şey değil. [Resmi belge](https://platform.claude.com/docs/en/build-with-claude/prompt-caching). | Context Compiler sabit prefix'i korumalı; sağlayıcı adapter'ı taşıma semantiğine karar vermeli. |
| `analiz1.md:53`: checkpoint tamamlama garantisi sağlar | Mantıksal olarak yanlış. Kayıt, kurtarmaya yardım eder; çözülebilirlik, ilerleme veya dış eylemin tekrar güvenliği garantisi vermez. | Recovery sözleşmesi ve sonuç belirsizliği ayrı modellenmeli. |
| `analiz1.md:78`: OpenCodeReview ~9 kat token tasarrufu, benzer başarı | Proje README'si ~1/9 token bildiriyor; ayrıca recall'ın daha düşük olduğunu özellikle belirtiyor. Sonuç kod inceleme görevine ve kendi benchmark'ına ait. [Proje](https://github.com/alibaba/open-code-review). | Bu oran genel kod üretimine taşınamaz; precision/recall ödünleşimi ve deney koşulları saklanmalı. |
| `analiz1.md:12,135`: LongCLI-Bench, 20 görev ve <%20 başarı | Resmi repository bu iddiaları bildiriyor. Bu, deneydeki ajanlar ve 20 görev için sonuçtur. [Proje](https://github.com/finyorko/longcli-bench). | Güncel bütün ajanlara genellenemez; küçük ve hedefli bir uzun görev eval'i olarak kullanılabilir. |
| `analiz2.md:15,34`: H-Mem, ikili ağaç ve %8.4 artış | İsim belirsiz: 2026 H-Mem zaman/anlam ağacı ve bilgi grafı kullanıyor; 2025 H-MEM ayrı çalışma. Açılan kaynaklar metindeki evrensel %8.4 iddiasını temellendirmiyor. [H-Mem 2026](https://arxiv.org/abs/2605.15701), [H-MEM 2025](https://arxiv.org/abs/2507.22925). | DOI/arXiv kimliği, görev, baseline ve ölçüm birimi olmadan oran mimari gerekçe olamaz. |
| `analiz1.md:21,43`: Trust Ratchet görev ilerledikçe yetki azaltır | Eksik anlatım. Cloudflare bunu politikada tanımlı korunan olaylarla, örneğin hassas veri okumasıyla ilişkilendiriyor; bütün yürütme grafında uygulanmasını istiyor. [Agent Access Model](https://blog.cloudflare.com/the-agent-access-model/). | Zaman/ilerleme sayacı yerine veri sınıflandırması ve sürümlü yetki bariyeri gerekir. |
| `idea.md:2774`: MCP stateless core yönü | 2026-07-28 resmi duyurusu bunu doğruluyor. [Sürüm duyurusu](https://blog.modelcontextprotocol.io/posts/2026-07-28/). | Üretim adapter'ını değişken `draft` URL'sine değil sürümü sabitlenmiş protokole bağla; capability negotiation yap. |

Scion'un container ve farklı harness orkestrasyonu iddiaları [resmi projede](https://github.com/googlecloudplatform/scion) karşılık buluyor. Ancak bir orkestratörün varlığı, bizim çekirdeğimizin bütün ajanların iç araçlarına ve bağlamına aynı düzeyde hükmedebileceğini göstermez. [Gas Town](https://github.com/gastownhall/gastown) da tasarım örneğidir; dayanıklılık garantilerini kendi uygulamamız için ayrıca kurmak gerekir.

`analiz2.md` içindeki Fable 5 / GLM 5.2 örneği, muğlak yazar atıfları, MemoBench/StoryBench ifadeleri ve `analiz1.md` içindeki DPACT iddiası açık yayın kimlikleriyle eşleştirilmedi. Bunları “yok” saymıyorum; doğrulanmış mimari dayanak olarak kullanmıyorum. `idea.md` §61–62'deki ContextOS / Context Manager projelerinin URL, sürüm veya kodu verilmediğinden bunlara ilişkin uygulama eleştirileri de bağımsız olarak doğrulanamadı.

### 2.2 Güçlü kaynaklardan ne alınabilir, ne çıkarılamaz?

| Kaynak | Desteklediği yön | Kanıtlamadığı şey |
|---|---|---|
| [MemGPT](https://arxiv.org/abs/2310.08560) | Hiyerarşik harici bellek ve sanal bağlam yaklaşımı | Sınırsız doğru hatırlama; bütün coding görevlerinde üstünlük |
| [RepoCoder](https://aclanthology.org/2023.emnlp-main.151/) | Repository-level completion için iteratif retrieval/generation | Uzun görevli runtime'ın recovery ve güvenlik doğruluğu |
| [CodePlan](https://www.microsoft.com/en-us/research/publication/codeplan-repository-level-coding-using-llms-and-planning-2/) | Repository değişikliklerinde bağımlılık ve artımlı planlama | Her görev için graph planner gerekliliği |
| [SWE-agent](https://arxiv.org/abs/2405.15793) | Araç arayüzünün ajan performansındaki rolü | Her model için tek ideal araç/prompt tasarımı |
| [LocAgent](https://aclanthology.org/2025.acl-long.426/) | Graph ile kod lokalizasyonu | Eksiksiz semantik etki grafı veya güvenli test eleme garantisi |
| [CAT](https://arxiv.org/abs/2512.22087) | Aktif context yönetimi ve eğitilmiş SWE-Compressor | Araç adını eklemenin eğitimsiz her modelde aynı kazanımı vermesi |
| [RLM](https://arxiv.org/abs/2512.24601) | Uzun girdiyi harici ortamda programatik inceleme ve recursive çağrılar | Sadece opaque referans eklenmiş her retrieval sisteminin RLM olması |
| [Repository Memory](https://proceedings.iclr.cc/paper_files/paper/2026/hash/b4c06f095368497f3ac19422efef8133-Abstract-Conference.html) | Geçmiş commit/issue bilgisinin lokalizasyona katkısı | Eski kararların bugün doğru veya bağlayıcı olması |
| [RPG](https://proceedings.iclr.cc/paper_files/paper/2026/hash/9482f45fdd89aba9130bb04c44f788a9-Abstract-Conference.html) | Sıfırdan repository üretiminde yapılandırılmış plan | Aynı maliyetle bütün mevcut repo bakım görevlerinde üstünlük |
| [SWERank](https://proceedings.iclr.cc/paper_files/paper/2026/hash/7f6901ebab786e43b21530328fc989ca-Abstract-Conference.html) | Eğitilmiş retrieve/rerank ile issue lokalizasyonu | Eğitilmemiş ağırlıklı skor formülünün aynı başarısı |
| [Compression reliability](https://arxiv.org/abs/2608.06503) | AppWorld üzerinde compaction sonrası davranışın ölçülmesi; erken deneysel sonuçlar | Checkpoint alanlarını kontrol etmenin davranışsal eşdeğerlik garantisi |
| [User-correction TRACE](https://arxiv.org/abs/2606.13174) | Kullanıcı düzeltmelerini kontrol kurallarına dönüştürme | Doğal dili hatasız policy'ye çevirme veya sıfır ihlal |
| [Context-attribution TRACE](https://arxiv.org/abs/2608.09153) | Trajectory'den context hatası teşhis ve düzeltme adayları | Her kullanıcı memnuniyetsizliğinin context hatasından kaynaklanması |

Üç ayrı çalışmada TRACE adı geçiyor; bibliography'de tam başlık ve arXiv kimliği kullanılmalı. Farklı görevlerdeki kazanımlar toplanıp bu ürünün beklenen kazanımı olarak sunulmamalı.

## 3. Mimari bulgular

P0: kodlamadan önce çözülmeli; veri, yetki veya yanlış tamamlanma riski. P1: ilk ürün tasarımında çözülmeli; güvenilirlik ve maliyet riski. P2: ileride iyileştirilebilir. Bunlar fikir dokümanı bulgularıdır, çalışan yazılımda kanıtlanmış açıklar değildir.

### F01 — P0: Birden fazla source of truth, çelişki çözüm kuralı olmadan tanımlanmış

Konum: [idea.md:70](C:/Users/ixayl/Desktop/harrnes/idea.md:70), §3.

Canlı dosya, index, test sonucu ve karar aynı otorite sınıfına konmuş. Dosya değişmişken index eskiyse hangisi gerçek? Test çıktısı yalnız çalıştırıldığı snapshot için gözlemdir; graph dosyadan türetilmiş bir görünüm.

**Düzeltme:** Kaynak gerçeklerini immutable snapshot, kullanıcı talimatı ve tool receipt olarak sakla. Task state'i journal'dan türet. Index, özet ve çıkarımları yeniden üretilebilir görünüm say. Çelişkide kaynak sürümü kazanır; model çıkarsaması otomatik otorite kazanmaz.

### F02 — P0: File SHA semantik güncellik için yeterli değil

Konum: [idea.md:350](C:/Users/ixayl/Desktop/harrnes/idea.md:350), §7, §24, §49.

`auth.ts` aynı kalırken lockfile, import edilen modül, environment veya DB şeması değişebilir. “Auth güvenli” iddiasının SHA'sı eşleşir ama dayanağı eskimiştir. Watcher olay kaybı, rename ve harici edit de ele alınmamış.

**Düzeltme:** Observation ve Claim ayrımı; claim için dependency fingerprint ve doğrulama kapsamı. Bilinmeyen bağımlılıkta `UNKNOWN`. Kritik karar öncesi diskle yeniden uzlaşma; watcher yalnız hızlandırıcı.

### F03 — P0: Kaynak göstermek, iddiayı doğrulamak değil

Konum: [idea.md:997](C:/Users/ixayl/Desktop/harrnes/idea.md:997), §23–24, §73.

Model yanlış satıra referans verip `confidence: 1.0` yazabilir. Schema bunun doğru yorum olduğunu kanıtlamaz. Aynı modelin verifier olarak tekrar onaylaması bağımsız kanıt da değildir.

**Düzeltme:** Runtime provenance'ı mühürler; agent yalnız claim önerir. `observed`, `inferred`, `tested`, `human_accepted` ayrılır. Sayısal confidence yalnız kalibrasyonu varsa kullanılır; authority değildir.

### F04 — P0: Transaction sınırı belirsiz

Konum: [idea.md:1127](C:/Users/ixayl/Desktop/harrnes/idea.md:1127), §28–30, §82.

İki dosyadan biri yazıldıktan sonra süreç çökerse veya shell DB'ye yazarsa “transactional apply” ne yapacak? SQLite commit'i dosya sistemi ve uzak API'yi aynı atomik işlem haline getirmez. [SQLite'ın atomik commit açıklaması](https://sqlite.org/atomiccommit.html) kendi depolama sınırını anlatır.

**Düzeltme:** İzole candidate snapshot + write-ahead operation journal + before/after hash + recovery. Tutarlı candidate pointer yayınlamayı, kullanıcının sıradan çalışma dizinine çok dosyalı aktarımından ayır. Harici işlemlerde idempotency/reconciliation kullan; küresel rollback vaat etme.

### F05 — P0: Shell ve testler yetki modelinin arkasından dolaşabilir

Konum: [idea.md:1468](C:/Users/ixayl/Desktop/harrnes/idea.md:1468), §40–41, §59.

`npm test *` izinli diye test salt okunur olmaz. Test script'i başka süreç başlatabilir, dosya silebilir veya ağ isteği yapabilir. String allowlist gerçek process yetkisini sınırlamaz.

**Düzeltme:** Process tree seviyesinde FS/ağ/credential sınırı. Komut ve argv policy'si ek kontrol olsun. Test kodu, hooks, formatter, package install ve LSP de çalıştırılabilir güvenilmeyen girdilerdir. `test` otomatik olarak düşük risk değildir; sınırlandırılmış ortamda otomatik çalışabilir.

### F06 — P0: Worktree güvenlik sandbox'ı değil

Konum: [idea.md:1182](C:/Users/ixayl/Desktop/harrnes/idea.md:1182), §30, §59.

Ayrı dizin, process'in başka dizine gitmesini önlemez. Git worktree'ler bazı ref/config durumlarını paylaşır. [Git belgesi](https://git-scm.com/docs/git-worktree).

**Düzeltme:** Workspace izolasyonu ve OS sandbox ayrı sözleşmeler. Git metadata yazıları kernel üzerinden yapılmalı; agent `.git` ortak durumuna doğrudan yazmamalı. Salt worktree kullanılan mod daha düşük izolasyon seviyesini açıkça bildirmeli.

### F07 — P0: Tamamlanma koşulu kendi eksik gereksinimini onaylayabilir

Konum: [idea.md:1240](C:/Users/ixayl/Desktop/harrnes/idea.md:1240), §32.

Model “reuse attack engellenecek” hedefinden concurrency koşulunu çıkarmazsa kendi ürettiği zayıf testlerle PASS alabilir. Predicate doğru hesaplanırken hedef yanlış modellenmiş olabilir.

**Düzeltme:** Kabul ölçütlerini ham kullanıcı isteğine bağla, değişikliklerini sürümle. PASS/FAIL/UNKNOWN/WAIVED durumları ve verifier türü tut. Model mevcut zorunlu kriteri düşüremez. Kritik davranışlarda bağımsız test/inceleme kullan. Completion, tanımlı kapsam için doğrulanır; genel doğruluk iddiası değildir.

### F08 — P0: Geçen test yanlış sürümün testi olabilir

Konum: §31–32, §66.

A ve B ayrı ayrı geçerken birleşimleri bozulabilir. Son testten sonra tek dosyalık edit bile eski PASS'i geçersiz kılabilir. Hedefli testlerin graph'a göre seçilmesi, eksik graph yüzünden regresyonları gizleyebilir.

**Düzeltme:** Verification receipt'i candidate snapshot, requirement version, policy, test harness ve environment'a bağla. Final candidate üzerinde tekrar doğrula. Bilinmeyen etki kapsamı testleri genişletir. Başlangıç arızalarını baseline'da sakla.

### F09 — P0: Çökme sonrası eylem sonucu belirsizliği yok

Konum: §49, §58, §82.

PR oluşturulduktan sonra yanıt kaybolursa replay ikinci PR açabilir. Bir komutun kaydı bulunmaması komutun çalışmadığını göstermez.

**Düzeltme:** Her operation için stable ID, intent, attempt ve receipt; güvenli tekrar sınıfı. `UNKNOWN_OUTCOME` açık durum olsun. Inspect/replay varsayılan olarak dış eylem çalıştırmasın.

### F10 — P0: Steering ile eski yetkili çağrı arasında yarış var

Konum: [idea.md:2247](C:/Users/ixayl/Desktop/harrnes/idea.md:2247), §68–69.

“Migration yapma” geldiğinde eski policy ile kuyruğa alınan migration hâlâ çalışabilir. Yalnız model interrupt etmek bu yarışı kapatmaz.

**Düzeltme:** Constraint version / policy epoch; dispatch, gerçek execution ve promotion öncesi kontrol. Eski lease'leri fence et. Başlamış geri alınamaz eylem için iptal garantisi verme; sonucu uzlaştır ve kullanıcıya bildir.

### F11 — P1: Transcript'in bütünüyle geçici sayılması kullanıcı niyetini kaybettirir

Konum: §3, §13, §48.

Ham kullanıcı mesajı, typed state çıkarıcısının yanlış yorumunu düzeltmek için birincil kaynaktır. Sadece son üretilmiş task spec'i saklamak niyetin sessizce değişmesini engellemez.

**Düzeltme:** Kullanıcı mesajları ve revision zinciri kalıcı input journal. Model konuşması state değildir ama arşivlenebilir. Kısıt çıkarımı source span ile bağlıdır. Belirsiz öneri, zorunlu kural olarak sessizce yürürlüğe girmez.

### F12 — P1: Her tur yeniden derleme, her tur hafızayı sıfırlama gibi uygulanabilir

Konum: §3.2, §11–15.

Yakın tool-call/result eşleşmelerini, provider continuation verisini ve yarım muhakeme işinin operasyonel bağlamını atmak kaliteyi bozar. Değişen prefix cache maliyetini de artırabilir.

**Düzeltme:** Stable prefix + typed task state + sınırlı, yüksek doğruluklu yakın etkileşim + seçilmiş kaynaklar. Context'i her çağrıda doğrula; her çağrıda bütün geçmişi yeniden özetleme. Sağlayıcıya özgü continuation alanlarını adapter korusun.

### F13 — P1: Compaction sırasında state'i yeniden çıkarmak gereksiz kayıp kapısı açıyor

Konum: §13–15.

Zaten typed kaydedilmiş test sonuçları ve kısıtları tekrar LLM ile trajectory'den çıkarmak otoriteyi ikinci kez modele verir. Semantic compaction'ın davranış kaybını schema kontrolü yakalayamaz.

**Düzeltme:** Typed state olay anında güncellensin. Checkpoint bunun deterministik snapshot'ı olsun. LLM yalnız anlatı/hipotez özetlesin; zorunlu alanları değiştiremesin. Behavioral continuation eval'i ayrıca yapılsın.

### F14 — P1: MMU benzetmesinin garanti çağrışımı fazla güçlü

Konum: §5–6, §74, §90.

OS, bilinen adresteki byte'ı getirir. Ajan bazen araması gereken kavramı dahi bilmez. Harici belleğin büyük olması erişim başarısını veya birden fazla kaynağı birlikte akıl yürütmeyi garanti etmez.

**Düzeltme:** MMU iç mimari adı olabilir. Kullanıcı vaadi “sürümlü, geri çağrılabilir görev hafızası” olsun. Retrieval kapsamı ve bilinmeyenler görünür olsun; sonsuz doğru bağlam iddiası kaldırılmalı.

### F15 — P1: Graph'ın farklı kenar türleri aynı güvenilirlikte değil

Konum: §8–10, §63–66.

Tree-sitter'da çağrı ifadesi görmek doğru çağrı hedefini çözmek değildir. Reflection, DI, generated code, conditional compilation ve config bağlantıları eksik kalabilir. Co-change nedensellik değildir.

**Düzeltme:** Her edge için extractor/version, snapshot, `syntactic/resolved/observed/inferred` türü ve coverage. `not_found` ile `known_absent` farklı olsun. Unsupported dilde lexical fallback; index engeli nedeniyle görev durmasın.

### F16 — P1: Ranker formülü eksik tanımlanmış

Konum: §10–12.

BM25, cosine, yakınlık ve recency ölçekleri uyumlu değil. Staleness'i sadece ceza puanı yapmak, çok benzer eski kanıtın güncel kanıtı yenmesine izin verir. Retrieval confidence'ın nasıl ölçüldüğü yok.

**Düzeltme:** Önce scope/sensitivity/freshness uygunluğu; sonra sıralama. Başlangıçta şeffaf rank fusion ve exact-match önceliği. Learned weights yalnız veri ve held-out eval ile. Düşük confidence yerine ölçülebilir coverage göstergeleri kullan.

### F17 — P1: Lease nesnesinde lease davranışı tanımlı değil

Konum: §21–22, §34.

TTL, renewal, generation/fencing token ve crash sonrası sahiplik olmadan iki agent aynı kapsamı sahiplenebilir. Symbol ownership formatter'ın bütün dosyayı değiştirmesini engellemez.

**Düzeltme:** İlk sürümde bir candidate'a tek yazar. Sonra file/package düzeyinde lease, stale generation reddi, izole candidate ve seri integration queue. Symbol sahipliği koordinasyon ipucu; güvenlik sınırı path/process seviyesindedir.

### F18 — P1: Multi-agent kazancı yalnız bağımsız dosyaya bakılarak seçilemez

Konum: §18–23, §72.

Test ajanı ile implementasyon ajanı farklı dosyalara yazsa da aynı belirsiz API'ye bağımlı olabilir. Portlar, DB, cache, lockfile ve global bütçe de shared state'tir. Farklı reviewer aynı yanlılığı paylaşabilir.

**Düzeltme:** Önce contract; sonra gerekiyorsa paralellik. Spawn kararında beklenen kritik yol kazancı eksi hazırlık, model ve birleştirme maliyeti. Bağımsız salt okuma analizi ilk aday; iç içe sınırsız delegation yok.

### F19 — P1: Model bağımsızlığı ile agent-CLI sarmalama birbirine karışmış

Konum: `analiz1.md:95`; `idea.md` §35–39.

Bir model endpoint'ini kullanırken araç yürütmesi çekirdekte kalır. Mevcut bir coding CLI'yi alt süreç olarak çalıştırınca iç shell, compaction ve permission davranışını aynı ayrıntıyla kontrol edemeyebilirsin.

**Düzeltme:** Core kendi agent loop'una sahip olsun. ModelAdapter ve ExternalAgentAdapter ayrı extension türleri. İkinci tür varsayılan olarak sandbox içinde patch üreten, daha sınırlı gözlemlenen worker'dır. “OpenAI-compatible” etiketi capability testi yerine geçmez.

### F20 — P1: Global bütçe var ama eşzamanlı rezervasyon yok

Konum: §34, §36–38.

İki çağrı aynı kalan bütçeyi okuyup birlikte aşabilir. Router switch cache'i kaybettirebilir; lokal model maliyeti sıfır değildir. Timeout sonrasında sağlayıcı ücretlendirmesi belirsiz olabilir.

**Düzeltme:** Atomik reserve/settle/release ledger; üst sınırla çağrı kabulü; provider/model sınırları ve switching maliyeti. Faturalama belirsizliğini açık kaydet. Kalite sınırı olan model, zor bir işi daha çok alt göreve bölmekle sınırsız telafi edilemez.

### F21 — P1: Watchdog kolay ölçülen ilerlemeyi gerçek ilerleme sanabilir

Konum: §33, §53.

Yeni evidence veya kabul edilmiş edit üretmek, hedefe yaklaşmadan sonsuz döngü yaratabilir. “Minimal diff” ödülü gerekli kapsamı eksiltmeyi teşvik edebilir. Kullanıcı düzeltmesi yeni gereksinim de olabilir.

**Düzeltme:** Açık obligation azalması, yeni test bilgisi ve tekrarlanan action fingerprint birlikte izlenmeli. Önce strateji değişimi, sonra sınırlı retry, ardından gerekçeli block/budget stop. Ham process sinyallerini başarı ödülü yapma.

### F22 — P1: Plugin invariant'ı process sınırı olmadan sözden ibaret

Konum: §42, §59–60, §87.

Aynı process'e yüklenen eklenti doğrudan dosya veya ağ açabiliyorsa Tool Kernel'i atlayabilir. Repo talimatı, MCP açıklaması veya kalıcı memory bir trust escalation kaynağı olabilir.

**Düzeltme:** Güvenilmeyen plugin ayrı sınırlandırılmış süreç ve capability API. Trusted in-process eklenti açık trusted-computing-base istisnasıdır. Repo içeriği ve belleği kendiliğinden yetki vermez. Secret filtreleme tek başına veri sızıntısı garantisi değildir.

### F23 — P1: Privacy modları birbirinden bağımsız kararları tek enum'a sıkıştırmış

Konum: §52–55, §60, §84.

Yerel görev kaydı, uzak modele gönderme, telemetry, training export ve saklama süresi farklı kararlardır. `LOCAL_ONLY` seçiliyken remote embedding kullanımı ihlal olabilir. Trajectory anonimleştirmesi kod/log içindeki kişisel bilgiyi garantiyle temizleyemez.

**Düzeltme:** Ayrı policy eksenleri; provider-aware egress filtresi; embedding/reranker dahil model çağrıları için aynı sınır. Yerel secret store ve erişim denetimi. Retention, export, deletion ve derived-index silme tasarımı baştan kurulmalı.

### F24 — P1: Ürün odağı, ölçülebilir kazanım ve kapsam sırası eksik

Konum: §70, §77–87.

90 bölüm içinde graph, daemon, training, routing, orchestration ve UI aynı vizyonda duruyor. “Core küçük olsun” bunu tek başına çözmüyor. Hedef kullanıcı, ilk kullanım, kurulum maliyeti, dil/OS matrisi ve vazgeçme kriteri yok.

**Düzeltme:** İlk hedef mevcut Git reposunda uzun bakım/değişiklik işi yapan geliştirici. Basit görev için hızlı yol; zor görev için durable yol. Önce tek agent güvenilirliği; sonra kanıtlanmış retrieval kazanımı; sonra paralellik. Her fazın shipping ve iptal kriteri ayrı.

### F25 — P1: Eval listesi var, karşılaştırma protokolü yok

Konum: §50–58, §78–80.

Farklı modeller, bütçeler, retry sayıları veya repository history erişimiyle harness katkısı ayrılamaz. Geçmiş commit retrieval, benchmark çözüm commit'ini içerebilir. Pass@10 ve pass@1 birbirine karışabilir.

**Düzeltme:** Aynı model/prompt bütçe koşullarında eşleştirilmiş deney; repo ve zaman bazlı ayrılmış holdout; history cutoff; çözülemeyen görevleri içeren toplam maliyet; ablation ve belirsizlik aralıkları. Kullanıcı kabulü tek başına doğruluk ölçümü değil.

### F26 — P2: Session/epoch ve UI terimleri kullanıcı işinden fazla yer kaplıyor

Konum: §43–48, §67, §85–86.

Development epoch, task, session, execution ve checkpoint kavramlarını kullanıcıya ayrı ayrı öğretmek gerekmez. Aynı engine için iki farklı process modelinin lifecycle davranışları belirsiz.

**Düzeltme:** UI'da görev, değişiklik, doğrulama ve beklenen karar göster. Runtime attempt/checkpoint içeride kalsın. Foreground ve background aynı kernel'i kullansın; detach, stop, cancel ve restore farklı, açık davranışlar olsun.

## 4. Analiz örnek kodundaki yürütme hataları

[analiz2.md:93](C:/Users/ixayl/Desktop/harrnes/analiz2.md:93) örneği kavramsal olsa da uygulamaya doğrudan taşınmamalı:

1. Sıkıştırma model çağrısından sonra yapılıyor; taşma kontrolü daha çağrıdan önce gerekli.
2. `MAX_WINDOW` bütçesinde output, tool schema ve sağlayıcı overhead rezervi yok.
3. Özet DB'ye yazılıyor ama dönen context'e eklenmiyor; sonraki çağrıda geri yükleneceği garanti değil.
4. İlk tool-call mesajı ve kullanıcı isteği ikinci model çağrısında doğru protokol zinciriyle korunmuyor.
5. Yalnız bir araç turu ele alınıyor; çok adımlı loop, cancellation, timeout ve retry yok.
6. Kullanıcı girdisi, tool intent/result ve state geçişi atomik görev günlüğüyle ilişkilendirilmemiş.

Doğru çözüm bu fonksiyona birkaç koşul eklemek değil; context oluşturma, provider protokolü ve dayanıklı operation yürütmesini ayrı sözleşmelere ayırmaktır.

## 5. Korunacaklar ve yeniden konumlandırılacaklar

**Korunacak:** local-first, typed task state, evidence provenance, optimistic concurrency, araç ergonomisi, seçici planlama, global bütçe, basit TUI, opt-in veri kullanımı, modele göre uyarlama, bütünleşik eval.

**Yeniden tanımlanacak:** Context MMU'nun garantileri, source-of-truth hiyerarşisi, completion predicate, workspace transaction sınırı, graph doğruluğu, compaction, lease ve security enforcement.

**Deney sonucu bekleyecek:** global vector index, öğrenilmiş ranker/router, symbol düzeyinde paralel yazım, otomatik skill öğrenimi, fine-tuning/RL, dağıtık daemon ve geniş plugin platformu.

Ürünün savunulabilir farkı, aynı görev ve model koşullarında daha az yeniden iş yaptırarak güvenilir sonuç üretmesi olmalı. Bunun teknik temeli, her önemli sonucun hangi istek, snapshot, ortam, izin ve doğrulama kapsamına ait olduğunu kaybetmemektir.
