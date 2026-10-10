# ADR 0019: Açık rıza ile managed family task content retention

10 Ekim 2026. Runtime uygulanmıştır; exact revision foundation kabulü [VALIDATION](../VALIDATION.md) içinde ayrı kaydedilir. Bu karar [ADR 0017](0017-privacy-checkpoint-design.md)'nin yalnız managed task content expiry dilimidir. Metadata checkpoint/compaction veya bütün privacy retention sistemi değildir.

## Yetki ve kullanıcı akışı

Default KEEP. Kullanıcı canonical UTC deadline, exact `DELETE_MANAGED_FAMILY_TASK_CONTENT_AFTER_DEADLINE_V1` acknowledgement, mevcut retention revision ve stable command ID verir. KEEP süre ve acknowledgement taşımaz. Politikalar USER actor, MANAGED_FAMILY_TASK_CONTENT scope, SYSTEM_UTC clock, authority digest, source physical owner root, task sequence ve document digest taşır. Model tool veya proje config'i timer silme yetkisi üretemez.

Politika external family privacy journal'ında family admission lock altında append edilir. Task reducer state ve resource ledger değişmez. Revision CAS ve command input digest geçmişteki exact retry'ı korur; farklı girdi veya revision gap reddedilir. External record rehash etmek, başka authority veya farklı input digest'i geçerli yapmaz. Pending deletion sonrasında politika değiştirilmez. Deletion'ın source ve restored-scope kernel command kimlikleri external intent tarafından önceden rezerve edilir; retention komutuyla namespace çakışması intent publication'dan önce reddedilir.

CLI `retention TASK --store STORE` status; `--keep` veya `--expires-at UTC --acknowledge CONSENT`, `--revision N --command-id ID` ile configuration; store-scope `retention --store STORE --run-due` maintenance sağlar. Owner IPC peer authentication kullanır; scope/payload/stable ID doğrulanır. TUI `/retention` aynı owner API'yi kullanır. Absent store yaratılmaz.

## Zamanlayıcı ve clock

Background owner başlangıçta ve her dakika, aktif invocation yokken maintenance dener. Her pass context'i 30 saniyedir; en fazla 16 due task attempt vardır. Deadline/task sırasındaki round-robin cursor, kalıcı olarak pinned erken görevlerin sonraki eligible görevi aç bırakmasını engeller. Cursor scheduling state'idir; silme yetkisi değildir. Restart durable politika ve immutable intent'ten yeniden tarar. Owner kapalıyken timer çalışmaz; explicit standalone pass mümkündür.

Canonical UTC RFC3339Nano timestamp 20–30 byte olarak bounded'dır; parser offset veya alternative formatting'i normalize etmez. Deadline consent zamanından sonra olmalıdır. SYSTEM_UTC işletim sistemi saatine güvenir; clock ileri alınırsa expiry hızlanabilir, geri alınırsa bekler. Current runtime clock ve observed expiry timestamp family lock altında yeniden doğrulanır; gelecekteki observation erken silme yetkisi değildir. Kernel event timestamp de deadline'dan önce olamaz. Güvenilir zaman sunucusu veya persistent monotonic deadline iddiası yoktur.

## Expiry admission, purge ve replay

Runtime due politika için mevcut deletion preview/admission'ı kullanır. Terminal execution, settled token/process resource reservations, descendant pins, family owners, source/root/content inventory ve current scope digests doğrulanır. UNKNOWN, active veya retained descendant durumunda BLOCKED döner; intent yayınlanmaz. Bounded hata kodları içerik/path/error body dökmez. Kullanıcının manuel deletion intent'i timer tarafından replay edilmez.

Expiry, immutable external command'a exact policy ve observation bağlar. Intent byte unlink'ten önce durable olur. Source ve restored kernel PENDING tombstone'u `retention` actor, policy digest/revision/deadline taşır; `user` actor bu alanlarla impersonation yapamaz. Kernel reducer şekil/actor/deadline/ledger invariants'ını doğrular; external policy'nin current authority kabulü trusted runtime family lock sınırındadır. Pure reducer external journal'a erişmez.

Purge bütün kayıtlı family içerik yollarında mevcut exact-inventory publication/purge kurallarını kullanır. Task/minimal risk ledger, cost ve quality geçmişini değiştirmez; başarı veya VERIFIED üretmez. Son immutable PURGED receipt olmadan tamamlandı denmez. Pending expiry restart'ta aynı original command/plan/digest/watermark ile devam eder; yeni plan, yeni authority veya ikinci watermark üretilmez. Eski backup restore external current watermark'ı geçemez.

## Kabul ve açık kapsam

Actual child death: external intent, kernel PENDING, ilk object unlink ve final PURGED öncesi dört sınır; fresh reopen exact intent retry. Actual UTC deadline timer, native CLI/TUI/owner routing, revision/dedup, early/future forgery, foreign authority/rehashed input/revision gap ve pending command namespace collisions. 17 görevde ilk 16 active iken sonraki eligible görevin ikinci pass'ta purge olması; UNKNOWN ledger ve retained descendant pin'leri. Backup + registered restored owner aynı retention tombstone'u taşır; policy intent sonrasında değişmez. Timestamp parser fuzz, full native OS/race ve privacy mandatory evidence gate regression sağlar.

Minimal metadata/audit retention, explicit raw redaction sırasında recovery kaybı, metadata checkpoint/compaction/migration, persistent owner retirement ve aggregate managed physical quota bu dilimde kapanmaz. Eski binary bilinmeyen RETENTION_POLICY kaydında fail-closed olur; sessiz policy ignore desteklenmez. Provider/unmanaged copy veya fiziksel medya erasure vaat edilmez.
