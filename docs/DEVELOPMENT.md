# Geliştirme ve offline kullanım

Go 1.27.1; tek Go module. Ürün sürümü 0.6.0-dev. Kararlı A–D release kapıları kapalıdır. Bu sürüm explicit offline fixture ile çalışan, candidate üzerinde değişiklik üreten bir engineering profilidir.

## Derleme

```sh
go mod download
go test ./...
go vet ./...
go build -trimpath -o bin/viber ./cmd/viber
./bin/viber doctor --json
```

Windows binary adı `bin/viber.exe`. Bu checkout'ta portable toolchain `.tools/go/bin/go.exe`, cache `.cache` altında. `scripts/check.ps1` format/mod/vet/test/build/doctor kontrollerini çalıştırır. Toolchain, cache, binary ve yerel store Git'e girmez.

## Çalışan fixture örneği

```sh
viber run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store ../viber-demo-store --task greeting --allow-unverified --json
viber status greeting --store ../viber-demo-store --json
viber diff greeting --store ../viber-demo-store --json
viber export greeting --store ../viber-demo-store --output ../viber-demo-delivery --json
```

İlk komut exit **2** verir: FINISHED / UNVERIFIED / SATISFIED, unresolved required verification obligation korunur. Kaynak dosya ve Git index değişmez. `--allow-unverified` verilmezse görev WAITING_USER (exit 3) durumunda, candidate'a bağlı limited-delivery request üretir. Modelin “verified” yazması kaliteyi değiştirmez.

`export`, değişen dosyaların exact before/after byte'larını, unified `changes.patch`, `report.json` ve en son `manifest.json` üretir. Raw prompt, model continuation ve tüm store export edilmez. Binary candidate değişikliği varsa exact bytes korunur; `patch_complete:false` ile text patch'in eksik kapsamı bildirilir. Export canlı kaynağa apply etmez ve görevin kalitesini yükseltmez. Çıktı yeni bir dizin olmalı; üst dizini önceden var olmalıdır.

Windows'ta derlemeden sonra `scripts/offline-demo.ps1` aynı akışı `.cache` içinde benzersiz store/delivery/backup/restore dizinleriyle çalıştırır.

## Review ve bağlı yanıt

`--autonomy review` her geçerli candidate proposal'ını, exact tool argümanlarıyla bekleyen isteğe dönüştürür. Salt okuma onay gerektirmez. `guided` ve `auto` bu sınırlı offline profilde yalnız native candidate araçlarını kullanır; shell/remote/live-write yetkisi vermez.

```sh
viber requests greeting --store ../viber-demo-store --json
viber respond greeting --store ../viber-demo-store --request REQUEST_ID --response-file response.json --json
viber resume greeting --store ../viber-demo-store --json
```

Response JSON, request'ten alınan bağları eksiksiz taşır:

```json
{
  "command_id": "unique-response-command",
  "task_id": "greeting",
  "request_id": "REQUEST_ID",
  "attempt": 1,
  "expected_spec_version": 1,
  "expected_policy_digest": "64_HEX_FROM_REQUEST",
  "action_digest": "64_HEX_FROM_REQUEST",
  "response": {"decision": "approve"}
}
```

Karar `approve` veya `reject`. Yanıt task/spec/policy/candidate/action/expiry bağını doğrular. Aynı command ID+payload kayıtlı sonucu döndürür; farklı payload COMMAND_ID_CONFLICT olur. Onay tek işleme aittir, sonraki proposal'a yayılmaz. Geçerlilik 24 saat; süresi dolan onay dispatch yetkisi vermez. `resume` onay değildir; `respond` da işlemi otomatik çalıştırmaz. Terminal attempt resume edilemez; yeni task gerekir.

## Recovery ve geçmiş

```sh
viber pause greeting --store ../viber-demo-store --json
viber cancel greeting --store ../viber-demo-store --json
viber inspect greeting --store ../viber-demo-store --at 1 --json
viber replay greeting --store ../viber-demo-store --until 1 --json
viber events greeting --store ../viber-demo-store --after 0 --limit 64 --json
```

Cursor **task_seq**'dir; global journal_seq ile karıştırılmaz. Sıfır inspect/replay için current state demektir. Replay retained journal'ın tüm bütünlüğünü kontrol eder ve dış işlem çalıştırmaz. Event paging 1–256 kayıt döndürür. Kontrol komutu exit 0 yalnız komut başarısıdır.

Admission öncesi intent ve üst sınır token rezervasyonu, işlem sonrası receipt/candidate pointer birlikte kaydedilir. Belirsiz model response/usage veya kesilmiş admitted işlem BLOCKED kalır; rezervasyon silinmez, kör retry yapılmaz. Resume canlı source baseline'ının değiştiğini görürse WAITING_USER olur; otomatik rebase/overwrite yoktur.

Bu sürüm peer-authenticated yerel owner kullanır. Çalışan `run`/ `resume` sırasında ikinci CLI `status`, `diff`, `pause`, `cancel`, `inspect`, `replay`, `events`, `requests` ve `respond` komutlarını aynı owner'a gönderir. Yeni writer açılmaz. Host effects/export/backup için IPC registry yoktur; aktif owner varken bu komutlar STORE_OWNED alır.

```sh
viber serve --store ../viber-demo-store --json
# Ayrı terminal:
viber status greeting --store ../viber-demo-store --json
viber resume greeting --store ../viber-demo-store --command-id run-001 --json
viber pause greeting --store ../viber-demo-store --command-id pause-001 --json
```

`serve` mevcut store için foreground owner açar; otomatik daemon/detach değildir. Windows user SID'li named pipe, Unix UID/PID doğrulamalı local socket kullanılır; TCP yoktur. Descriptor ve response owner ID/generation ile bağlıdır. Aynı command ID tarihsel sonucu döndürür; farklı komuta reuse çatışmadır. Gönderilmiş komutun response'u kaybolursa UNKNOWN_OPERATION_OUTCOME döner; otomatik retry/fallback yapılmaz. Önce status/events ile reconcile edilir. Admission receipt var ama completion yoksa aynı resume ID yeniden çalıştırılmaz. Ulaşılamayan eski descriptor OS owner kilidi altında açılarak toparlanabilir.

Ctrl+C foreground invocation'ını PAUSED bırakır ve exit 130 döner. Owner kapanışı da admission durdurup bounded pause/drain yapar. Unknown reservations korunur; cancel ayrı, açık CANCELLED komutudur. Terminal task eski outcome'u değiştirerek resume edilemez.

## Ham steering ve scope revision

```sh
viber steer greeting "Yeni talimatın exact ham metni" --store ../viber-demo-store --command-id input-001 --json
viber revise greeting --store ../viber-demo-store --revision-file revision.json --json
viber resume greeting --store ../viber-demo-store --command-id revised-run-001 --json
```

Steering önce ham input blob'unu ve journal barrier'ını kalıcı yapar, sonra aktif native loop'u duraklatır. Semantik onay veya implicit resume değildir. Aynı steering command ID tekrar etki yaratmaz. Pending input çözülmeden work admission kapalıdır.

Revision JSON:

```json
{
  "command_id": "scope-001",
  "task_id": "greeting",
  "input_id": "input-001",
  "expected_spec_version": 1,
  "expected_policy_epoch": 1,
  "expected_candidate": "64_HEX_FROM_STATUS",
  "fixture_bytes": "BASE64_OF_FRESH_OFFLINE_FIXTURE"
}
```

İlk pending input explicit current bindings ile çözülür. Spec ve policy epoch yükselir; önceki source-span requirements/protected origin, candidate ve kullanılan/reserved bütçe korunur. Yeni required criterion eklenir. Eski request/onaylar ve model continuation geçersizleşir; limited delivery yeniden explicit onay gerektirir. Başka pending input varsa bariyer devam eder. Unknown effect scope revision ile silinmez. Fresh fixture, kullanıcı tercihindeki offline planning girdisidir; gerçek modelin yeni talimatı planlaması yerine geçmez. `revise` coding çalıştırmaz.

## Native paging ve context sınırı

Native fs_read/file/list/search yanıtları explicit coverage ve next_cursor taşır. Read default 16 KiB/en çok 64 KiB; list 64/256; search 32/128 kayıt. Cursor aynı tool/path veya query/limit/candidate/policy ile kullanılır; source veya authority değişince STALE_BASE olur. Cursor izin yerine geçmez; pending input yeni read admission'ını da durdurur.

fs_read exact_bytes base64 verisi authoritative'dir. content yalnız geçerli UTF-8 segmentte görünür; split rune/binary replacement yapılmaz. offset ile random hydration için candidate_digest zorunludur. File read condition tüm dosyanın precondition hash'idir; page coverage ayrıca bildirilir. Binary baseline dosyaları mevcut capture profilinde excluded kalır; binary candidate proposal byte'ları sayfalanabilir.

fs_list FILE/DIRECTORY/EXCLUDED kayıtlarını birlikte sayfalar. Bir sayfada olmayan dosya yokluk kanıtı değildir. fs_search 2048-byte excerpt ile match/line/excerpt byte offsets taşır; excerpt_complete:false uzun satırın tamamının görülmediğini belirtir. Arama yalnız captured policy scope'u tarar, excluded dosyalarda yokluk iddia etmez.

Offline context profili bütün raw intent/spec/restrictions/tools/paired history'yi mandatory tutar. 512 KiB conservative byte upper bound, 512 output reserve, 4096 margin vardır; gerçek provider tokenizer değildir. Sığmayan required context WAITING_RESOURCE + CONTEXT_TOO_SMALL olur; fixture dispatch, steps ve reservation ilerlemez. Context kırpılmaz.

Successful preflight request'i ve manifest'i durable saklanır. run/status JSON'daki context alanı profile, estimate, included/omitted IDs ve request/manifest digest'lerini gösterir; raw request metni yayınlanmaz. Eksik request blob'u veya değişmiş manifest load/backup'ı reddeder. Scope revision yeni context gerektirir. Compaction/production provider count ve semantic goal coverage halen tamamlanmadı.


## Yedek ve temiz geri yükleme

```sh
viber store-backup --store ../viber-demo-store --output ../viber-demo-backup --json
viber store-restore --backup ../viber-demo-backup --store ../viber-demo-restored --json
```

Yeni çıktı path'i ve mevcut üst dizin gerekir. SQLite WAL dosyası kopyalanmaz; owner kilidi altında [SQLite VACUUM INTO](https://www.sqlite.org/lang_vacuum.html#vacuum_with_an_into_clause) ile tutarlı DB snapshot'ı alınır. Historical task documents, ham niyet, fixture/response blob'ları ve snapshot closure doğrulanır. Blob/DB dosyaları sync edildikten sonra `backup.json` yayınlanır. Restore bütün digest/size/path/schema sınırlarını doğrular, fresh stage'de replay+closure denetler ve mevcut dizini değiştirmeden yayınlar.

Profil limiti: 512 MiB toplam, 256 MiB DB, 32.768 dosya. Locks, geçici dosyalar ve yeniden üretilebilir candidate materialization kopyalanmaz. Retention/delete henüz olmadığı için `UNSUPPORTED_NO_DELETIONS`, watermark 0 zorunludur; bu, tombstone veya privacy deletion conformance iddiası değildir. Restore pending effect'i veya approval'ı otomatik çalıştırmaz. Yedek aynı makinede, aynı source kimliğiyle kurtarma içindir; başka makinedeki source yolunu otomatik rebinding yapmaz. Backup hash'leri bütünlük kontrolüdür, dışarıdan gelen bir yedeğin kimliğini doğrulayan imza değildir.

## Snapshot ve sandbox sınırları

`snapshot-save --git`, host Git helper/config/filter çalıştırmadan SHA-1/SHA-256 index v2/v3/v4, dirty/staged/untracked bytes, linked worktree metadata, nested .gitignore ve info/exclude okur. Global excludesFile okunmaz. Unmerged/sparse/split index, symlink/gitlink/special/hardlink ve unsupported ignore şekilleri fail-closed olur. Native capture iki tarama yapar ancak **BEST_EFFORT**'tur; atomik historical FS görüntüsü değildir.

Store root Unix 0700 veya Windows user+SYSTEM private DACL kullanır. Snapshot/archive/model authority kernel tarafındadır. Host readonly chmod gerçek sandbox assurance sayılmaz. `sandbox-run` explicit operator profile/policy/authority ile pinned, önceden kurulu Linux Docker image'ında non-root, network none, read-only root/source, private tmpfs ve kaynak limitleri uygular. Docker socket/credential mount edilmez. Output bounded; timeout/cancel sonrası container ağacı temizlenir. Exit 0 ve stdout PASS güçlü verification receipt değildir. Tam escape/egress/fencing/OS power-loss conformance kapıları açıktır.

OpenAI Responses, Anthropic Messages ve Ollama adapter'ları canonical protocol fixture testleriyle geliştirilmiştir. Kullanıcı tercihiyle gerçek inference yapılmadı; CLI yalnız `--offline --fixture` açar. Streaming, gerçek endpoint kabulü, keychain ve provider loop bağlantısı tamamlanmadı.

## Henüz desteklenmeyenler

Live apply/workspace restore, semantic spec/check-origin review, protected check observer, tam budget/control reserve, tam IPC platform conformance/supervisor/attach/TUI, provider semantic steering, compaction, retention/delete ve tam disk/control reserve, imzalı paketleme ve bağımsız pilot kabulü tamamlanmadı. Bunlar için host shell veya daha zayıf güvenlik fallback'i yoktur. [Plan](IMPLEMENTATION_PLAN.md), [release gates](RELEASE_GATES.md) ve [ölçülen doğrulama](VALIDATION.md) ayrı tutulur.

## Korunan kontrol planı (0.5 engineering)

İlk candidate değişikliğinden önce trusted operator check/helper/config/fixture kapsamını bildirebilir. Plan argv'si şu anda çalıştırılmaz; expected discovery bir sonuç beyanı değildir. Plan/model çıktısı VERIFIED yetkisi vermez. Plan yoksa typed origin `UNRESOLVED` olur.

Örnek `check-plan.json` (runner_digest gerçek planlı runner metadata digest'iyle doldurulur; aşağıdaki sıfırlar yalnız biçim örneğidir):

```json
{
  "schema_version": 1,
  "checks": [{
    "id": "behavior-suite",
    "kind": "TEST",
    "requirement_ids": ["user-goal"],
    "argv": ["trusted-runner", "--selection", "all"],
    "runner_digest": "0000000000000000000000000000000000000000000000000000000000000000",
    "selection": "all registered behavior checks",
    "expected_tests": ["behavior-case-1"],
    "closure": [
      {"path": "tests", "recursive": true},
      {"path": "test.config", "recursive": false},
      {"path": "extra.config", "recursive": false}
    ]
  }]
}
```

```powershell
.\bin\viber.exe run "TASK" --offline --fixture fixture.json --check-plan check-plan.json --root C:\source --store C:\private-store --json
```

Path bir file ise recursive false, bir directory/tree ise true olmalıdır. Absent file/tree kapsamı sonradan gizli config/test eklenmesini de engeller. Closure scope exclusions/sensitive/binary kaynaklarla kesişirse eksik veri üzerinden güvence vermek yerine reddedilir. TEST için boş/duplicate expected discovery ve bilinmeyen requirement IDs kabul edilmez. Plan 1 MiB, 128 checks, check başına 256 scopes ve toplam 1024 distinct scope ile bounded'dır. Komut/env içinde credential bulundurmayın; production credential handle entegrasyonu ayrı D1 işidir.

Origin durable task document'te tutulur. Sonradan aynı plan dosyasını değiştirmek mevcut görevi değiştirmez. Guided/auto ve review approval aynı protected guard'a tabidir. Mevcut revision original origin'i korur; yeni kriter eski coverage'ı kullanamaz. Korunan test beklentilerini değiştirme protokolü henüz sunulmuyor; böyle bir öneri POLICY_DENIED olur. Yeni agent testini kapsam dışında eklemek eski protected check yerine geçtiğini göstermez.

`check_protection` JSON/IPC/export özeti origin digest, provenance, closure coverage, check/scope count ve `strong_verification_available: false` içerir. Raw operator argv/discovery listesi status'a açılmaz. Provenance `OPERATOR_DECLARED_REPOSITORY_BASELINE` bağımsız test oracle'ı değildir; `EXPLICIT_UNREVIEWED` tam dependency closure kabulü değildir. Candidate ve protected scope digests compiler'a zorunlu girdi olarak girer. Offline fixture teslimleri UNVERIFIED/exit 2 olmaya devam eder.

[ADR 0005](adr/0005-protected-check-origin.md), [üretim tamamlama planı](IMPLEMENTATION_PLAN.md) ve [release kapıları](RELEASE_GATES.md) kabul sınırını tanımlar.
## Store sürüm geçişi (0.6 engineering)

Yeni store schema 2, eski store schema 1 olarak açılır. Normal run/status/restore otomatik migration yapmaz; reducer/event/task version değişmez.

~~~sh
viber store-migration-status --store ../viber-demo-store --json
viber store-migrate --store ../viber-demo-store --backup-output ../viber-before-upgrade --command-id upgrade-20261008 --json
~~~

Store'u tutan serve veya başka owner önce kapatılmalıdır; migration owner RPC üzerinden dispatch edilmez. Bütün task'lar terminal ve pending/UNKNOWN effect/reservation'sız olmalıdır. Pause migration için yeterli değildir. Unknown çağrıyı tekrar deneyerek veya reservation'ı silerek geçiş engeli aşılmaz.

--backup-output mevcut parent altında fresh directory veya aynı journal'ın tamamlanmış v1 yedeğidir. Yedek raw input/CAS/journal closure ile private temp store'a gerçekten restore edilir; geçici doğrulama kopyası sonra temizlenir. Source/store/backup overlap reddedilir. Migration yalnız v1→v2'dir; v2 store'da yeni upgrade isteği UNSUPPORTED döner.

Kesinti halinde aynı --command-id ve original backup path ile aynı komut tekrar çalıştırılır. store-migration-status pending intent, schema ve read-only reason'ı verir. Pending açılış generation yükseltmez; run/create/steer/respond/revise/checkpoint durur; inspect/replay açık kalır. Commit gerçekleşmişse DDL tekrarlanmaz. Complete retry original receipt'i döndürür; farklı backup/request command ID conflict olur. Bozuk marker veya kayıp original backup'ta dosyaları silmeyin; doğrulanmış yedekten fresh store-restore kullanın.

Schema 2 snapshot format manifest digest'ini taşır. Migration'lı v2 backup completion metadata'sını da korur; v1 backup v1 olarak restore edilir. Store/backup aynı host source identity içindir; cross-host path rebinding desteklenmez. Disk preflight DB'nin 3 katı +32 MiB ister; fiziksel control reserve ve elektrik kesintisi conformance iddiası değildir. Retention/delete/tombstone kabulü açık kalır.

[ADR 0006](adr/0006-store-migration.md) transaction, crash ve kabul sınırlarını tanımlar.
Önceki schema 1 tam yedeğinizle ayrı native CLI süreçleri üzerinden upgrade/restore smoke çalıştırmak için:

~~~powershell
.\scripts\migration-demo.ps1 -LegacyBackup C:\private\viber-v1-backup -Task greeting
~~~

Script original yedeği değiştirmez; fresh .cache output altında eski restore, migration öncesi/sonrası backup, yeni restore ve historical replay oluşturur. Pending/unknown task veya yanlış task ID aynı güvenli preflight'te reddedilir.