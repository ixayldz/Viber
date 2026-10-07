# Geliştirme ve offline kullanım

Go 1.27.1; tek Go module. Ürün sürümü 0.3.0-dev. Kararlı A–D release kapıları kapalıdır. Bu sürüm explicit offline fixture ile çalışan, candidate üzerinde değişiklik üreten bir engineering profilidir.

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

Live apply/workspace restore, semantic spec/check-origin review, protected check observer, tam budget/control reserve, tam IPC platform conformance/supervisor/attach/TUI, provider semantic steering, compaction, migration/retention/delete, imzalı paketleme ve bağımsız pilot kabulü tamamlanmadı. Bunlar için host shell veya daha zayıf güvenlik fallback'i yoktur. [Plan](IMPLEMENTATION_PLAN.md), [release gates](RELEASE_GATES.md) ve [ölçülen doğrulama](VALIDATION.md) ayrı tutulur.
