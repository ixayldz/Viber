# Source-bound retrieval ve intent-aware log kullanımı

Native fs_search varsayılan olarak mevcut literal aramayı kullanır; ilk dosya okuması index yaratılmasını beklemez. Agent/model tool çağrısında ranked mod açıkça seçilir:

~~~json
{"query":"RefreshAccessToken","mode":"ranked","intent":"IDENTIFIER","limit":3}
~~~

IDENTIFIER tam FTS phrase; ERROR, FEATURE, REFACTOR en fazla 16 token ile OR expansion kullanır. Öncelik her zaman case-sensitive literal kaynak/path eşleşmesidir. Sonra BM25 ve case-sensitive trigram listeleri reciprocal rank fusion ile birleştirilir; rank değerleri ve kullanılan sinyaller görünür. Her dosyadan en iyi tek chunk döner. Index çıktısı kaynak hakkında güvenilmeyen bir öneridir; kod semantiği, requirement coverage veya verification kanıtı değildir. [SQLite FTS5 mekanikleri](https://sqlite.org/fts5.html).

Index yalnız capture içindeki policy ve aktif plan read scope tarafından kabul edilen byte'ları içerir. Manifest candidate/policy/source-set hash'lerini, index sürümünü, gerçek SQLite page maliyetini ve süreyi taşır. Her hit dosya SHA'sı, byte start/end, line ve base64 exact bytes içerir. Binary kaynaklar ayrı sayılır; policy dışı kaynaklar index'e ve kapsam istatistiklerine girmez.

coverage.next_cursor aynı query/mode/intent/limit ile kullanılır. Candidate, policy veya scope değişince cursor reddedilir. Ranked pool en fazla 1024 dosyadır; FTS listeleri 512 chunk ile sınırlıdır. Truncation ve boş sonuç yokluk kanıtı değildir. Kaynaklar en fazla 32 MiB/10000 dosya/32768 chunk; chunk 4096 byte, overlap 512 byte. Query 256 byte, limit 128 hit; ham snippet üst sınırı 512 KiB, JSON/base64 overhead ayrıca vardır.

Mevcut uygulama her ranked çağrıda private RAM SQLite index'ini yeniden kurar ve kapatır. Kalıcı/warm index cache yoktur; benchmark'taki warm query yalnız index nesnesinin yeniden kullanım maliyetini izole eder. Repo SQLite/WAL, extension veya model SQL'i çalıştırılmaz. Build 10 saniye, query 3 saniye ve parent cancellation ile sınırlıdır. Desteklenmeyen/bütçeye sığmayan ilk build explicit literal fallback döndürür; ranked cursor literal offset olarak yorumlanmaz. SQLite page üst sınırı 128 MiB; clone/chunk/Go allocation maliyeti ayrıca vardır ve süreç RSS için hard quota iddiası değildir.

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

Her çalıştırma benzersiz .cache/context-benchmark-* dizinine environment, JSONL golden test ve allocation benchmark çıktısı yazar. Cold index+query ve warm query ayrıdır. 66 dosyalık sentetik corpus'un 6 önceden ilan edilmiş identifier/error/substring/feature/Unicode relevance etiketi recall@3 ile ölçülür. Sonuç byte-span validity ve stale/policy rejection ayrı testlerdir. 480064-byte noisy build log fixture'ında tail-only kesit erken root error'ı kaçırırken intent selection exact root span'ı 512-byte bütçede korur.

Bu corpus coverage/pilot p95 veya model coding task başarı oranı değildir. Gerçek provider anahtarı, abonelik kabulü veya gerçek repo performans eşiği bu testlerle tamamlanmış sayılmaz.
