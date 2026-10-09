# Source-bound retrieval ve intent-aware log kullanımı

Native fs_search varsayılan olarak mevcut literal aramayı kullanır; ilk dosya okuması index yaratılmasını beklemez. Agent/model tool çağrısında ranked mod açıkça seçilir:

~~~json
{"query":"RefreshAccessToken","mode":"ranked","intent":"IDENTIFIER","limit":3}
~~~

IDENTIFIER tam FTS phrase; ERROR, FEATURE, REFACTOR en fazla 16 token ile OR expansion kullanır. Öncelik case-sensitive literal içerik eşleşmesindedir; path-only eşleşme ardından gelir ve dosya başına yalnız bir path sinyali oluşturur. BM25/trigram yalnız body column'unda arar; uzun bir path dosyanın tüm chunk'larını öne çıkarmaz. Rank listeleri integer reciprocal rank fusion ile birleştirilir; kullanılan sinyaller görünür. Her dosyadan en fazla üç örtüşmeyen chunk döner; bu limitte kesilen ek ilgili span'lar truncation olarak bildirilir. V2 index sürümü eski ranked cursor'ı reddeder. Index çıktısı kaynak hakkında güvenilmeyen bir öneridir; kod semantiği, requirement coverage veya verification kanıtı değildir. [SQLite FTS5 mekanikleri](https://sqlite.org/fts5.html).

Index yalnız capture içindeki policy ve aktif plan read scope tarafından kabul edilen byte'ları içerir. Manifest candidate/policy/source-set hash'lerini, index sürümünü, gerçek SQLite page maliyetini ve süreyi taşır. Her hit dosya SHA'sı, byte start/end, line ve base64 exact bytes içerir. Binary kaynaklar ayrı sayılır; policy dışı kaynaklar index'e ve kapsam istatistiklerine girmez.

coverage.next_cursor aynı query/mode/intent/limit ile kullanılır. Candidate, policy veya scope değişince cursor reddedilir. Ranked pool en fazla 1024 span'dır; FTS listeleri 512 chunk ile sınırlıdır. Truncation ve boş sonuç yokluk kanıtı değildir. Kaynaklar en fazla 32 MiB/10000 dosya/32768 chunk; chunk 4096 byte, overlap 512 byte. Query 256 byte, limit 128 hit; ham snippet üst sınırı 512 KiB, JSON/base64 overhead ayrıca vardır.

Owner session private RAM index'lerini task/candidate/policy/version ve bağımsız source-set digest'ine bağlı cache'te tutar. Her warm çağrıda policy filtering ve bütün yetkili kaynakların SHA doğrulaması tekrar yapılır. Başka task cache'i paylaşmaz; source/policy değişimi eski entry'yi retire eder. Candidate publication, spec revision, finalization ve owner close explicit invalidation yapar; 2 dakikalık idle TTL background sweep ile byte'ları bırakır. En fazla 4 resident index/128 MiB conservative source+chunk+SQLite estimate ve bir transient build vardır. Aktif retired okuyucu son release'e kadar kota hesabında kalır; kapatılan owner veya revoked scope sırasında biten build publish edilmez. Estimate cache admission ölçüsüdür; süreç RSS hard limit değildir. Kapasiteye sığmayan build typed literal fallback'e gider.

Repo SQLite/WAL, extension veya model SQL'i çalıştırılmaz. Build 10 saniye, query 3 saniye ve parent cancellation ile sınırlıdır. Desteklenmeyen/bütçeye sığmayan ilk build explicit literal fallback döndürür; ranked cursor literal offset olarak yorumlanmaz. SQLite page üst sınırı index başına 128 MiB; clone/chunk/Go allocation ve transient build maliyeti ayrıca vardır.

Ordinary check çıktısını intent ile seçmek:

~~~json
{"run_id":"unit-run","stream":"stderr","offset":0,"limit":1024,"intent":"BUILD"}
~~~

BUILD, TEST, ERROR, DIAGNOSTIC hata, fail/assert, stack veya warning satırlarını ve komşularını öne çıkarır. Limit seçilen ham byte bütçesidir; JSON/base64/provenance maliyeti context sayımına ayrıca girer. Receipt digest, candidate, retained stream SHA, exact span, omitted byte, duplicate ve truncation bilgisi döner. En fazla 8 MiB retained source/65536 taranan satır/64 seçilen satır; uzun satırın sınıflandırması ilk 4096 byte ile sınırlıdır. Seçim eksik gözlem olarak kalır. Normal exact byte paging intent olmadan kullanılabilir.

Independent observer çıktısı aynı policy nedeniyle modelden gizlidir; intent seçimi bu kuralı açmaz. Log içindeki PASS veya exit zero goal verification üretmez. Kernel/protocol/constraints mandatory context blokları budanmaz.

## Anahtarsız effectiveness ve maliyet ölçümü

~~~powershell
./scripts/context-benchmark.ps1 -BenchTime 1s
~~~

Her çalıştırma benzersiz .cache/context-benchmark-* dizinine environment, JSONL golden test ve allocation benchmark çıktısı yazar. Cold index+query, standalone warm query ve gerçek owner warm araması (policy/hash/cache/hydration dahil) ayrıdır. 66 dosyalık sentetik corpus'un 6 önceden ilan edilmiş identifier/error/substring/feature/Unicode relevance etiketi recall@3 ile ölçülür. Üretim repository kaynaklarında bağımsız ilan edilen 6 declaration etiketi recall@5, exact span validity ve source değişimiyle cache reddi ölçülür. Owner benchmark OS process-wide peak resident counter'ını da bildirir; bu test runner/capture/setup dahil toplamdır, yalnız cache'e atfedilmez ve referans donanım kabulü değildir. Sonuç byte-span validity ve stale/policy rejection ayrı testlerdir. 480064-byte noisy build log fixture'ında tail-only kesit erken root error'ı kaçırırken intent selection exact root span'ı 512-byte bütçede korur.

Bu corpus coverage/pilot p95 veya model coding task başarı oranı değildir. Gerçek provider anahtarı, abonelik kabulü veya gerçek repo performans eşiği bu testlerle tamamlanmış sayılmaz.
