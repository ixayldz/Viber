# Görev içeriğinin silinmesi

`delete-preview` ve `delete` gerçek görev içeriğini siler; API anahtarı istemez. İşlem geri alınamaz. Model ve otomatik GC bu yetkiyi kullanamaz.

## Kullanım

Görev terminal durumda olmalıdır. Pending/UNKNOWN etkiler, unsettled rezervasyonlar ve başka bir açık restore owner'ı işlemi engeller. Türetilmiş görevler önce silinir; parent belgesi onların doğrulama kanıtıdır.

```powershell
$plan = .\bin\viber.exe delete-preview greeting --store $store --json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Silme önizlemesi alınamadı.' }
# objects, managed_copies, restored_stores ve staged_attempts alanlarını inceleyin.
# Paylaşılan backup/support paketi bütün dosyalarıyla silinebilir.
@{ command_id = 'delete-greeting-001'; plan = $plan } |
    ConvertTo-Json -Depth 64 | Set-Content -Encoding utf8NoBOM .\delete-command.json
.\bin\viber.exe delete greeting --store $store --command-file .\delete-command.json --json
```

CLI ve background owner aynı sözleşmeyi kullanır. Komuttaki task, CLI task'ıyla aynı olmalıdır. Hata/bağlantı kopmasından sonra **aynı dosya ve command ID** ile yeniden deneyin. Değişmiş ID/plan eski intent'i devralamaz. Yeni işlem güncel preview gerektirir.

## Gerçek temizleme kapsamı

- Görevin raw input, model mesajı/yanıtı, log, context history/compaction/pin, document, receipt ve snapshot CAS dosyaları.
- Published ve yarım candidate materialization dosyaları.
- Kayıtlı store backup, changeset export, delivery preview ve support türevleri; son marker oluşmamış allocation ve geçici dosyalar dahil.
- Aynı authority'ye bağlı, bu sürümün restore ettiği private store'larda aynı görev içeriği; her kopya kendi journal cursor/document ve exact dosya envanteriyle plana bağlanır.
- Restore ilk içerik yazımından önce allocation kaydı oluşturur; henüz journal açılamayan yarım restore stage'leri, SQLite yan dosyaları ve control reserve dahil plana alınır.
- New attempt ilk inherited input kopyalamadan önce parent/cursor/document/request ve fiziksel owner lineage'ını durable kaydeder. TaskCreated öncesi çöken attempt raw dosyaları parent silme manifest'ine girer. Published child parent'ı pin eder; önce child silinir. Kayıtlı staged ID başka Create/request tarafından devralınamaz; parent silindikten sonra yeniden yayımlanamaz.
- Retrieval cache scope'u iptal edilir; aktif lease belleği okuyucu bırakınca kapanır. Fiziksel bellek overwrite garantisi verilmez.

Source workspace ve Git index silinmez. Her dosya private root altında single-link regular file, SHA-256/size ve fiziksel root kimliği ile kontrol edilir. Değişmiş/yabancı dosya veya root replacement `PURGED` sonucunu engeller. Recursive host path silme yapılmaz; boş dizinler kalabilir.

Minimal journal zinciri ve global charged/reserved muhasebe korunur. Tarihsel quality/outcome audit değeridir; silinen görev current başarı olarak sunulmaz. Current/historical içerik, raw history/queue, export ve yeni task/steering publication silme sınırını aşamaz. Developer archive komutları gerçek session CAS'ını supplied authority JSON ile açamaz.

## Authority, backup ve recovery

Store SQLite metadata CAS içinde write-once bir authority pin'i tutar. Ayrı private `.viber-privacy-…` kardeş dizini backup'ın içine kopyalanmaz. Fiziksel dizin kimliği/genesis sabittir; journal hash bağlı, contiguous ve kota sınırlıdır. Intent/watermark **unlink'ten önce** sync edilir.

External authority ayrıca random byte'larla gerçekten allocate edilmiş 32 MiB `control.reserve` tutar. Ordinary allocation/lineage kayıtları 1536 kayıt ve 32 MiB metadata sınıfıyla sınırlıdır; deletion intent toplam 2048 kayıt/64 MiB sınıfını kullanabilir. Publication temporary byte'ları da admission hesabındadır. Owner katalogu ordinary yeni kayıtlar için 7680, toplam okuma için 8192 entry ile sınırlıdır; admission lease dosyası oluşmadan kontrol edilir. Başarısız yeni owner/restore allocation katalogu şişirip mevcut owner'ın reopen/deletion yolunu kapatamaz. Work düşük disk sınırında durur; bounded control write physical reserve'i kullanabilir. `support --json` içindeki `privacy_metadata_capacity` yalnız sayısal anlık kapasiteyi gösterir. Bu sonlu rezerv sonsuz sayıda task/deletion garantisi veya managed copy payload'ları için toplam disk quota değildir. Legacy registry zaten control kapasitesini tüketmişse geçmiş kayıtlar otomatik silinmez; işlem durur.

Reserve pinned `os.Root` handle'ından açılır. Windows free-space query textual root path yerine açık handle'ın volume GUID'sini kullanır; root pathname replacement başka volume/dizinden kapasite veya yazma yetkisi sağlayamaz. Native Windows'ta açık root handle rename/replacement'ı fence eder; Unix acceptance pathname değişse de pinned root'a allocation yapılmasını doğrular. Windows free-space profili local volume GUID ister; desteklenmeyen filesystem için eski textual path'e sessiz fallback yapılmaz. Fiziksel reserve replacement/oversize/sparse allocation kabul edilmez.

Current backup schema 2 `CURRENT_EXTERNAL_AUTHORITY_REQUIRED` taşır. Restore mevcut watermark ve kayıtlı exact manifest/root kimliğini kontrol eder. Eski watermark, eksik/yer değiştirmiş authority ve kayıt dışı kopya trusted restore olamaz. Authority'yi taşıma/silme veya eski registry snapshot'ını geri koyma desteklenen recovery değildir.

Eski schema 1 backup manifest'i normal `store-restore` tarafından reddedilir. Mevcut schema 1 store `store-migrate` ile yükseltilir; bound migration recovery backup'ı geçici private alanda doğrulanır. Bu istisna public legacy restore veya eski payload ile task çalıştırma yetkisi değildir.

## Sonuç ve açık kapsam

`privacy-capacity --store STORE --json` ve TUI `/capacity`, numeric kapasiteyle birlikte sabit blocker/next-action kodlarını gösterir. `--replenish-control-reserve` veya `/capacity replenish` aynı current physical authority/family lock altında yalnız bounded control reserve'i doldurur; disk low watermark/physical readback doğrulanır. Missing store yaratılmaz, task/payload parametresi kabul edilmez. Fresh status işlem sonrasındaki durumu yeniden ölçer. Kernel snapshot, resource ledger, journal chain/watermark, owners ve process fence pin'leri değişmez.

Bu eylem journal/catalog compaction veya silme değildir. `WORK_JOURNAL_FULL_CHECKPOINT_REQUIRED`, `WORK_METADATA_FULL_CHECKPOINT_REQUIRED` veya `OWNER_CATALOG_FULL_RETIREMENT_REQUIRED` kalıyorsa ordinary work yeniden açılmaz. `supported_next_actions` uygulanmamış pruning/compaction komutu önermez. Sayısal `work_ready` yalnız `work_probe_bytes:4096` boyutlu anlık gözlemdir; gelecekteki publication/deletion izni veya control yolunun sınırsız garantisi değildir.

Authority ilk açılışta reserve/genesis yayınlanmadan önce exact directory/physical identity ile owner journal'ına kaydedilir. Kurulum kesintisinden sonra normal CLI/owner açılışı aynı pending allocation'ı tamamlar. Başka root veya farklı/foreign içerik benimsenmez; failed initialization sessiz yeni authority yaratmaz. Bind current pin'i ve pending marker'ının kaldırılmasını tek transaction'da yapar. Dizinleri prefix'e göre elle topluca silmek recovery değildir. Eski sürümlerden kalan kayıt dışı orphan'lar ve mkdir-before-allocation küçük directory artıkları otomatik cleanup kapsamına girmez; [ADR 0016](adr/0016-privacy-authority-allocation.md).

`PURGED_MANAGED_LOCAL_CONTENT` yalnız plana bağlı local içerik, kayıtlı ordinary derivative ve restore scope'ları için verilir. Hata sırasında external intent erişimi kapalı tutar, tombstone `PENDING` kalır; aynı komutla devam edilir.

Yeni native evaluator aynı external authority altında ayrı owner ve benzersiz `eval-…` task ID alır. İlk candidate CAS yazımından önce parent/cursor/document/request/physical owner lineage kaydedilir. Aktif original/evaluator/restore owner silmeyi engeller; published evaluator önce silinir. TaskCreated öncesi çöken evaluator kopyaları parent manifest'ine girer. `source.json`, `registration.json`, `result.json`, `manifest.json` raporları ilk yazımdan önce kayıtlı allocation kapsamındadır; nested owner ayrıca task-scope inventory ve minimal journal ile yönetilir. Yabancı rapor dosyası veya owner root replacement temizleme sonucunu engeller. Evaluator backup/restore aynı watermark ve family silme kontrolüne bağlıdır.

Otomatik TTL retention, metadata/audit retention, farklı scope'lardaki aynı içerik için content-wide deletion ve eski/kayıt dışı kopyalar K06'nın kalan işidir. Önceki ayrı authority kullanan schema-1 evaluator bundle'ları geriye dönük otomatik adopted/purged sayılmaz. Provider-side silme yapılmaz. SSD/OS kalıntısı, page cache, crash dump veya bağımsız kullanıcı kopyaları için fiziksel erasure iddiası yoktur.
