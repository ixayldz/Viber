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

Current backup schema 2 `CURRENT_EXTERNAL_AUTHORITY_REQUIRED` taşır. Restore mevcut watermark ve kayıtlı exact manifest/root kimliğini kontrol eder. Eski watermark, eksik/yer değiştirmiş authority ve kayıt dışı kopya trusted restore olamaz. Authority'yi taşıma/silme veya eski registry snapshot'ını geri koyma desteklenen recovery değildir.

Eski schema 1 backup manifest'i normal `store-restore` tarafından reddedilir. Mevcut schema 1 store `store-migrate` ile yükseltilir; bound migration recovery backup'ı geçici private alanda doğrulanır. Bu istisna public legacy restore veya eski payload ile task çalıştırma yetkisi değildir.

## Sonuç ve açık kapsam

`PURGED_MANAGED_LOCAL_CONTENT` yalnız plana bağlı local içerik, kayıtlı ordinary derivative ve restore scope'ları için verilir. Hata sırasında external intent erişimi kapalı tutar, tombstone `PENDING` kalır; aynı komutla devam edilir.

Otomatik TTL retention, metadata/audit retention, farklı scope'lardaki aynı içerik için content-wide deletion, eski/kayıt dışı kopyalar ve ayrı authority kullanan hidden-eval bundle lineage K06'nın kalan işidir. Provider-side silme yapılmaz. SSD/OS kalıntısı, page cache, crash dump veya bağımsız kullanıcı kopyaları için fiziksel erasure iddiası yoktur.
