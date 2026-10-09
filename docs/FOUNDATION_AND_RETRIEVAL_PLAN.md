# CI, process fencing ve Context MMU çalışma planı

2026-10-09. Kullanıcı sırası: önce güvenlik/CI, sonra fencing backend kabulü, sonra retrieval. Yeni installer, signing, update veya release kabiliyeti bu çalışma kapsamında eklenmez. `release_ready=false` değişmez.

## 1. Foundation kapısı

- Go 1.27.2 portable Windows arşivi resmi SHA256 ile doğrulanır: `1314008898bd40df77af4b014f777f08873dbdfbcd3d92308728ee03304fe04f`.
- Resmi Go Docker imajı digest ile sabittir: `golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61`.
- Üç OS native CI: module verify, format, vet, reachable vulnerability scan, tam test, üç tekrarlı migration regression, binary ve doctor.
- Linux: tüm paketlerin race testi ve derlenmiş bütün fuzz target'larının keşfi/çalıştırılması. Fuzz süreli bug aramasıdır; hatasızlık ispatı değildir.
- Ayrı Docker işi üç native OS işi geçmeden başlamaz. Runner, CLI, agent ve bağımsız evaluator gerçek engine üzerinde çalışır. Zorunlu testler SKIP olursa evidence doğrulayıcı işi düşürür.
- `foundation` işi her önceki işin success sonucunu ister. Branch protection için required check adı `foundation` olmalıdır; workflow dosyası repository yönetim ayarlarını değiştirmez.
- JSONL/log artifact'ları exact commit'e bağlanır. Yerel eski Go sonuçları yeni CI PASS yerine kullanılamaz.
- Önceki `8b7b7bb` CI run `37952267728`: üç OS reachable vulnerability scan'de Go 1.27.1 standart kütüphane açıklarıyla FAILED. Hiçbir scan devre dışı bırakılmadı.
- Go 1.27.2 yerel reachable scan: PASS, no vulnerabilities found. Windows migration: üç tekrar, 38 benzersiz test/subtest, sıfır skip/fail.
- Toolchain/image değişiminde örnek planın runner digest'i yeniden bağlanır; doğrulama zayıflatılmaz.

Kaynaklar: [Go resmi indirme/checksum](https://go.dev/dl/), [Go release history](https://go.dev/doc/devel/release), [eski kırmızı CI](https://github.com/ixayldz/Viber/actions/runs/37952267728).

## 2. Process-fencing kabulü

- Yeni admission kalıcı engine name fence kapasitesini hesaba katar. Fence temizleme otomatik olmaz; eski create/start taleplerinin artık gelemediği teknik olarak kanıtlanmadan name yeniden kullanılamaz.
- Engine capability ölçümü seccomp, rootless, cgroup version/driver ve CPU/memory/PID desteğini ayrı bildirir. Rootless `cgroup=none` kaynak sınırlarını uygulamıyorsa fail closed; rootless etiketi tek başına kabul değildir.
- Gerçek hostile süreçler: child/grandchild, timeout, output flood, PID/scratch/resource limit ve read-only/no-network sınırları. Sonuçta protected engine receipt ve quiescence denetlenir.
- Delayed create, lost ack, engine outage/reconnect/restart ve kapasite dolması: hiçbir incomplete cleanup PASS olamaz; kontrol reconciliation kapasite doldu diye engellenmez.
- Engine restart testleri yalnız bu kabul için yaratılmış disposable daemon'a uygulanır. Kullanıcının Docker Desktop/host daemon'u yeniden başlatılmaz.
- Native/rootless/disposable restart sonuçları ayrı kaydedilir. Desteklenmeyen rootless profil accepted diye yazılmaz.

Kaynak: [Docker rootless resource constraints](https://docs.docker.com/engine/security/rootless/tips/).

## 3. Context MMU retrieval

- Yalnız immutable, policy-admitted candidate bytes üzerinden source-bound SQLite FTS5/BM25 ve trigram index. Index authority değildir.
- Candidate, policy, extractor/index sürümü, dosya SHA ve exact byte span bağları. Eski index/cursor hard rejection; her result exact source ile hydrate edilir.
- Literal identifier/path/error araması öncelikli. BM25 ve trigram listelerinin şeffaf rank fusion/dedup'u; farklı score ölçeklerini kör toplama yok.
- Bounded indexing/query/outputs, cancellation, binary/excluded coverage; privacy dışı path adı/istatistiği sızmaz. İlk useful action full index'e bağlı olmaz; direct lexical fallback kalır.
- Log processing intent bazında hata/test/stack/diagnostic satırlarını seçer; exact raw ref/span ve omission bilgisini korur. PASS çıktısı verification yetkisine dönüştürülmez.
- Offline bağımsız relevance etiketleriyle recall@k, exact-source validity, stale rejection, cold/warm query/index süresi, allocation/index maliyeti ve log/context bütçe etkinliği ölçülür. Sentetik corpus sonuçları gerçek coding task success veya paired continuation kanıtı sayılmaz.

Kaynak: [SQLite FTS5/BM25/trigram](https://sqlite.org/fts5.html).
