# ADR 0020 — Açık seçilmiş proje belleği kopyası

12 Ekim 2026. PRD FR-14/16/26/29 ve privacy descendant closure. Bu belge çalışma sözleşmesidir; kabul sonuçları [VALIDATION](../VALIDATION.md) içinde ayrıca kaydedilir.

## Karar

Kullanıcı aynı owner store içindeki başka bir görevin tek ACTIVE `USER_ASSERTED` kaydını exact task/ID/digest ile hedef göreve kopyalar. Otomatik proje genelinde bellek keşfi açılmaz. Aynı proje, current retained/effective policy ve epoch koşulları gerekir; `context.read` iki görevde, `context.edit` hedefte doğrulanır. Kaynakta local-only/provider/egress restriction bulunması hedef izinleriyle aşılamaz.

Kopya ayrı immutable archive nesnesidir; `ProjectMemoryRef.type=PROJECT_MEMORY_COPY`, evidence class `USER_COPIED`. Origin ve target cursor/document/baseline/candidate/project/policy bağlarını, seçilen assertion kimliğini ve exact içerik hash'ini korur. Bu metin yetki, check receipt veya VERIFIED sonucu değildir. Kopyalanmış kaydın tekrar kopyalanması reddedilir; task descendant graph'ında döngü oluşturulmaz.

Görev başına en çok sekiz kopya, mevcut 256 kayıt geçmişi ve 16 KiB record sınırı paylaşılır. Kaynak assertion metni en çok 8 KiB'dir. Model bağlamı mevcut toplam item/byte sınırını kullanır; taşan bilgi sessizce tamamlandı sayılmaz.

## Yayın ve recovery

1. Stable user command ve exact request digest'i doğrulanır. Current source/target ve source assertion okunur; quiescent ve settled durum gerekir.
2. External privacy authority'de `MEMORY_COPY_PREPARED` source pin'i kalıcı olur. Source/target scope, copy/request hash'leri ve beklenen target publication cursor'ı kayıtlıdır.
3. Hedef archive body/document yayımlanır. `ProjectMemoryRecorded`/`COPY` kernel işlemi exact eski document/sequence/generation üzerinden yeni pointer ve monoton copy counter'ını kaydeder. Execution, spec, policy, quality ve work budget değişmez.
4. Exact target journal publication doğrulandıktan sonra `MEMORY_COPY_ACTIVE` kalıcı olur. ACTIVE bulunmadan context load bu ref'i benimsemez.

Kesinti halinde PREPARED pin'i korunur. Aynı command ID/request exact işlemi tamamlar veya drift durumunda durur; yeni kopya/pin üretmez. Başarılı tekrar aynı tarihsel `COPIED` receipt'ini verir. Güncel silinme durumu bu receipt'i değiştirmez; task status'tan okunur. Başarılı receipt, içerik hâlâ mevcut veya dış kopyalar silindi anlamına gelmez.

## Erişim ve silme

Her context/search kullanımı current hedef ve origin erişim/freshness koşullarını yeniden uygular. Kaynağın correction/retire/conflict, candidate/policy değişimi veya erişim reddi kopyayı aktif bilgi olarak kullanamaz. Origin okunamıyorsa saklanan metin üzerinden restriction aşılmaz; sonuç redacted/stale veya explicit omission olur.

PREPARED dahil retained hedef kopyası origin deletion'ı pinler. Hedefin içerik purge'u ve exact durable tombstone'u görülmeden origin purge'a izin verilmez. Kayıtlı backup/restored owner kapsamı mevcut family deletion closure'ıyla birlikte değerlendirilir; başka root'un scope kimliği sessizce benimsenmez. Privacy checkpoint, copy dedup/lineage ve source record sequence'lerini korur. Kopyayı retire etmek fiziksel purge yerine geçmez.

## Paket uyumluluğu ve sınır

Yeni biçim `project_memory_copy_v1` gerektirir; base `project_memory_v1` de zorunludur. Counter PURGED task'ta korunur. Install/rollback, registered store owner lock'larını selection publication'a kadar tutar; history/ref/counter yanında current external registry PREPARED/ACTIVE kayıtlarını authoritative reader ile tarar. Eski pakete sessiz downgrade yapılmaz.

Registry sorgusu point-in-time'dır. Registry lock'unu peer-store owner lock'larıyla ters sırada tutmak deadlock üretebilir. Aynı registered store writer'ı kilitlerle engellenir; farklı/unregistered shared-authority writer'ı ve legacy child owner handoff için atomik bütünlük iddiası yoktur. Bu sınır full lifecycle release gate'ini kapatmaz.

Cross-owner/multi-project automatic memory, untracked user/provider copy erasure, metadata/audit retention, semantic truth veya genel model AgentLease bu kararın kapsamı değildir.
