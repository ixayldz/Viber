# ADR 0017: Privacy checkpoint/retention için kabul tasarımı

10 Ekim 2026. **Checkpoint/compaction/migration ÖNERİ; bu backend runtime henüz uygulanmadı.** [ADR 0016](0016-privacy-authority-allocation.md) allocation crash yolunu kapatır; managed task content expiry ayrı [ADR 0019](0019-managed-content-retention.md) ile uygulanmıştır. Bu ADR kalan metadata compaction/retention/migration kararını önceden sabitler. Yalnız bir JSON snapshot yazmak journal silme yetkisi sağlamaz.

## Authority ve storage formatı

Hedef backend current external physical root altında bounded SQLite metadata checkpoint + append tail'dir. Genesis format/version açık ve owner journal'ının write-once authority pin'ine bağlı olmalıdır. Eksik DB/checkpoint/current anchor eski JSON journal'a veya boş registry'ye sessiz downgrade yapamaz. Legacy JSON authority kendi formatında okunur; yeni formatı kazanmak operator komutuyla bound migration gerektirir. Private filesystem/SQLite opening trusted owner sınırıdır; untrusted process'e DB/root verilmez.

Checkpoint canonical state, sequence/tail, watermark, source genesis ve integrity digest taşır. Append canonical record body + hash ve current head'i tek transaction'da günceller. Replay retained checkpoint ve tail'i doğrular; final sequence/tail/watermark current anchor ile aynı olmalıdır. Body/digest, format, root ve sequence gaps fail-closed olur. Fiziksel metadata/temp/page bütçesi ve SQLite durability/profile readback zorunludur. Başka textual root'a açılan DB aynı physical allocation sayılmaz.

## State'i küçültme koşulu

- Pending deletion tam exact plan/command taşır; recovery/UNKNOWN kaydı varsayılan olarak pin edilir.
- Tamamlanmış deletion yalnız final physical purge ve current kernel `PURGED` kanıtından sonra minimal task identity/watermark/command digest/result facts olarak tutulabilir. Aynı ID/payload retry aynı minimal sonucu üretir, farklı command reddedilir. Bu completion fact ayrıca durable registry transaction gerektirir; task ledger ve eski kalite beyanı değişmez.
- Managed copy yalnız bütün bağlı task içerikleri purge edilmiş ve registry-owned physical envanter bunu doğrulamışsa retired olabilir. Foreign file veya replaced root presence tamamlanmış purge diye kabul edilmez.
- Attempt/evaluator lineage yalnız parent/child access/reuse/old-backup revocation sözleşmesi başka durable minimal kayıtta korunuyorsa küçültülür. Deletion task ID namespace'i yeni owner altında tekrar kullanılabilir hale getirilemez.
- Owner lease/scope retirement aktif owner, restore stage, backup, evaluator ve process fence pin'lerini denetler. Bir lock pathname'ini kaldırıp aynı anda ikinci inode üzerinde writer yaratmak yasaktır; retirement family admission kilidi altında platform primitive'iyle ölçülür.

Sequence veya watermark compaction sırasında sıfırlanmaz. Gerekli dedup/current-policy/UNKNOWN risk'in retention süresinin sona ermesi açık availability ve effect-reconciliation sonucu verir; kör retry veya yeni SUCCESS üretmez.

## Legacy migration ve publication

Migration current store snapshot, external sequence/tail/watermark, owner-scope digest, format/genesis ve target physical root'a bağlı durable capsule ile başlar. Family admission durdurulur; mevcut bütün owner/operation leases quiescent olmalıdır. Eski binary'nin legacy journal okumaya devam etmesi engellenir. Source records değişmeden target hazırlanır ve bütçe dahilinde doğrulanır.

Birden fazla owner SQLite DB'si için tek ortak transaction yoktur. Migration intent her ara durumda normal admission'ı fail-closed tutar; per-scope CAS update ve exact recovery receipts gerekir. Current external genesis/head publication tüm scope'lar tutarlı olduktan sonra yapılır. Old records ancak target durability/replay ve required current anchor kabulünden sonra kaldırılır. Eski backup eski descriptor/watermark ile yeni formatı restore edemez; sessiz metadata rebinding yapılmaz.

Current publication sonrasında cleanup yarıda kalırsa tekrar target'ı current state diye okuyup yalnız recorded source inventory'yi retired eder. Source ve target marker'ları çelişirse automatic authority selection yapılmaz. SQLite checkpoint, kernel task journal'ına veya context compaction'a eşdeğer değildir.

## Retention ayrı yetkidir

Default retention otomatik irreversible deletion yetkisi vermez. Kernel expiry actor açık retention policy revision, deadline/clock semantics, scope ve current task binding taşır. User deletion ile timer expiry ayrı command/event admission'ıdır. Active workers/leases quiescent olmadan hard purge yapılmaz. Explicit sensitive raw redaction, minimal UNKNOWN ledger/tombstone'u korur ve recovery kaybını açık bildirir. Metadata/audit, task content, telemetry/training ve cross-project memory ayrı policy eksenleridir.

## Zorunlu kabul

1. Legacy→target migration ve checkpoint publication'ın her transaction/sync/name/head/scope/retire sınırında gerçek child death/reopen; old/new mixed scopes safe denial/recovery.
2. 2048 records, 64 MiB metadata ve 8192 catalog baseline; compaction sonrası ordinary admission tekrar kullanılabilir olmalı. Unresolved intent ve conservative risk küçülmez.
3. Missing/replaced DB/checkpoint/format/current anchor, rehashed foreign projection, old backup, raw manifest mismatch ve current watermark regression reddi.
4. Exact duplicate command ve changed payload; terminal `PURGED` receipt olmadan full plan retirement reddi; published/unpublished attempt/evaluator lineage korunumu.
5. 10 reopen/replay ve repeated compaction equality; disk pressure ve temp/page admission; control reserve exhaustion son durable pointer'ı korur.
6. Replay/admission latency/allocations/logical bytes 0/256/1024/1536/2048 ölçümü. Fiziksel IO ve p95 ayrıca ölçülür; `ns/op` veya logical byte bunlara dönüştürülmez.
7. Üç native OS, Linux race/fuzz ve current rootful Docker; gerçek engine restart/fencing regression ve desteklenen rootless resource-controller profili. Bu hosttaki rootless UNSUPPORTED sonucu başarılı backend kabulü değildir.

Bu tasarım tek başına implementasyon değildir. Global managed payload quota, operator recovery UX, legacy bundle migration, native power-loss ve bağımsız pilot diğer paketlerin kabulüne bağlıdır.
