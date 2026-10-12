# Anahtarsız çalışma yüzeyleri

Aşağıda ayrıca main geliştirmesi olarak belirtilen yeni komutlar haricinde bu kılavuzun komutları [12 Ekim engineering paketinde](https://github.com/ixayldz/Viber/releases/tag/engineering-5e6a006ff6c0cde18caa8ba48e9341075275d6eb) bulunur. Exact source `5e6a006ff6c0cde18caa8ba48e9341075275d6eb`; [16 CI işi](https://github.com/ixayldz/Viber/actions/runs/38224054501) başarılıdır. Üç platformdaki gerçek yönetilen kurulum kabulü ve public indirme/provenance readback'i geçti. Stable readiness ayrı PRD kapısıdır.

## Yönetilen kurulum

Main'deki yeni `/memory`, `/workers`, slash palette ve captured instruction profilleri için [CLI referansını](CLI_REFERENCE.md#proje-talimatları-bellek-ve-yerel-analizler) okuyun. Bunlar public `5e6a006` paketinden sonraki geliştirmelerdir. Yeni managed pakette kullanılan memory/read-worker capability'sini okuyamayan eski binary'ye rollback engellenir; yeni manifesti tanımayan bootstrap yerine yeni doğrulanmış portable binary gerekir.

İlk bootstrap binary'sini doğrulanmış portable paketten ayrı bir dizine açın. `package install` aynı byte snapshot'ında GitHub provenance'ını repo/workflow/ref/full SHA ile doğrular; checksum tek başına yeterli değildir. `--gh` PATH yerine operatörün seçtiği GitHub CLI'nin tam yoludur.

```powershell
$program = Join-Path $env:LOCALAPPDATA 'ViberApps\managed'
$archive = 'C:\Downloads\viber-SHA-windows-amd64.zip'
$revision = 'PAKETIN_TAM_40_KARAKTER_COMMIT_SHA_DEGERI'
$ghBinary = 'C:\Program Files\GitHub CLI\gh.exe'
.\viber.exe package install --prefix $program --archive $archive --source-sha $revision --gh $ghBinary --json
.\viber.exe package status --prefix $program --json
.\viber.exe package run --prefix $program -- version
```

Linux/macOS Terminal:

```sh
program="$HOME/.local/share/viber/apps/managed"
archive="$HOME/Downloads/viber-SHA-linux-amd64.tar.gz" # macOS: darwin-arm64
revision='PAKETIN_TAM_40_KARAKTER_COMMIT_SHA_DEGERI'
gh_binary="$(command -v gh)"
./viber package install --prefix "$program" --archive "$archive" --source-sha "$revision" --gh "$gh_binary" --json
./viber package status --prefix "$program" --json
./viber package run --prefix "$program" -- version
```

`archive` ve `revision` değerlerini indirdiğiniz yayımlanmış sürüme göre değiştirin. `gh_binary` boşsa önce GitHub CLI'yi kurun. Komutlar doğrulanıp açılmış bootstrap paketinin klasöründe çalıştırılır; bootstrap klasörü `program` dışında kalmalıdır.

Store'ları prefix dışında tutun. Yönetilen launcher, child komutunun açık `--store` yolunu kaydeder; yeni store'u child'dan önce hazırlar. Sonraki install/rollback/uninstall, kayıtlı store'ların tamamının fiziksel kimliğini doğrular ve OS owner kilitlerini alır. Aktif owner kilidi alınamıyorsa sürüm geçişi reddedilir. Doğrudan binary ile kullanılan başka store'ları `--store` ile ayrıca kaydedin; launcher bilmediği store'ların quiescence'ını iddia etmez.

Yönetilen sürümü ChatGPT ile kullanmak için aynı launcher üzerinden giriş yapın ve görevi başlatın. Giriş model API anahtarı istemez; hesap/workspace plan kullanım izni gerekir. Gerçek hesap kabulünün durumu [README](../README.md) içinde belirtilir.

Windows PowerShell:

```powershell
$project = 'C:\Projects\my-app'
$store = Join-Path $env:LOCALAPPDATA 'ViberData\my-app'
.\viber.exe package run --prefix $program -- auth login
.\viber.exe package run --prefix $program -- auth status
.\viber.exe package run --prefix $program -- run 'README kurulum adımlarını incele ve düzelt' --provider chatgpt --allow-remote --root $project --store $store --task first-task --autonomy review --allow-unverified
.\viber.exe package run --prefix $program -- ui first-task --store $store
```

Linux/macOS Terminal:

```sh
project="$HOME/projects/my-app"
store="$HOME/.local/share/viber/tasks/my-app"
./viber package run --prefix "$program" -- auth login
./viber package run --prefix "$program" -- auth status
./viber package run --prefix "$program" -- run 'README kurulum adımlarını incele ve düzelt' --provider chatgpt --allow-remote --root "$project" --store "$store" --task first-task --autonomy review --allow-unverified
./viber package run --prefix "$program" -- ui first-task --store "$store"
```

`project` yolunu kendi kodunuza göre değiştirin. `ui` içinde `/requests`, `/respond REQUEST_ID approve`, `/resume` ve `/diff` komutlarıyla inceleyin. Sonraki görevler için farklı task kimliği kullanın. Güncellemeden önce çalışan owner'ı durdurun; launcher store yolunu sonraki kurulum/rollback/kaldırma kontrolleri için kaydeder.

```powershell
.\viber.exe package install --prefix $program --archive $archive --source-sha $revision --gh $ghBinary --store C:\ViberData\project --json
.\viber.exe package rollback --prefix $program --revision ONCEDEN_KURULMUS_TAM_SHA --json
.\viber.exe package deactivate --prefix $program --json
.\viber.exe package uninstall --prefix $program --json
```

Rollback yalnız önceden doğrulanmış kurulum kaydı bulunan ve mevcut store/reducer sözleşmesiyle uyumlu sürümü seçer. Schema migration/downgrade yapmaz. Deactivate seçimden çıkarır ve dosyaları saklar. Uninstall exact inventory ve kalıcı retirement kaydıyla yalnız program dosyalarını siler; kesintiden sonra aynı komutla devam eder. Değişmiş/eklenmiş dosyaları silme yetkisi saymaz. Audit/seçim/store kayıtları kalır. Retire edilmiş aynı revision'ı yeniden kurmak için yeni prefix kullanın. Windows'ta uninstall için çalışan bootstrap prefix dışında olmalıdır.

Üç native CI işi gerçek GitHub provenance ile yeni paketi kurdu, ilk offline görevi çalıştırdı; aynı revision reinstall/rollback ve owned uninstall sırasında dış store korundu. İmzalı platform `*-installation.json` dosyaları public Assets bölümündedir. Public `90bc97` başlangıç sürümünden bu exact source'a üç platformda actual OIDC ile upgrade → rollback → upgrade PASS; mevcut task projection korundu. Store formatı migration/downgrade, OS sertifika/notarization ve gerçek model başarısı bu kabulün kapsamı değildir.

## Gizlilik checkpoint'i ve fiziksel kota

```sh
viber privacy-capacity --store /private/store --json
viber privacy-checkpoint --store /private/store --json
```

Checkpoint bakımı için family owner'larını durdurun. Lossless projection; dedup, pending deletion, UNKNOWN, managed-copy, attempt, consent ve watermark kayıtlarını taşır. Tüm kayıtlı owner SQLite pin'leri PREPARED→ACTIVE geçer; current anchor yayımlanmadan kaynak journal silinmez. Sonra exact kaynak aralığı için immutable retirement receipt yayımlanır ve bound inode/byte silmesi yapılır. Eksik kaynak, yalnız current anchor ve receipt doğrulanırsa kabul edilir. Checkpoint/catalog geçmişi bounded olarak tutulur; bütün metadata'nın sınırsız kullanımını veya otomatik owner katalog temizliğini vaat etmez.

Yeni owner ve fresh restore, current checkpoint pin'ine bağlanır. Restore, kaynak anchor soyunu ve exact registered staging kimliğini doğrulayarak destination SQLite pin'ini rebase eder; işlem kesilirse aynı owned staging kaydından devam eder. Bir task silinirken sibling task'ların export kopyaları korunur. Live backup/restore operation lease veya yarım restore stage varken checkpoint engellenir. Checkpoint sonunda complete bounded scan ile current-watermark backup pin'leri korunur; kullanılmayan eski checkpoint projection ve retirement receipt'leri identity/digest bağıyla silinir. Scan belirsiz veya limit dışıysa `historical_retirement_pending` ve reason döner. Anchor soyu ve owner catalog kayıtları korunur; catalog/legacy owner retirement ayrı kalan iştir. Kayıp family owner'ı checkpoint/catalog geçişini engelleyebilir; metadata/owner dosyalarını elle silmek recovery değildir.

Ortak fiziksel ledger, authority/DB kimliğine bağlıdır. Yazıcı reserve→WRITING→measured RETAINED akışını kullanır; kesilmiş yazı üst sınırı charged kalır. Snapshot, CAS/materialization, backup/export ve evaluator bundle bu yola bağlanır. Owner SQLite/lock/bounded WAL/SHM için retained metric receipt ve durably charged standby kullanılır; mutation öncesi standby WRITING olur, exact inode/physical ölçüm upper içinde kalırsa rearm edilir. Shrink high-water charge'i düşürmez. Bu namespace başka task dosyalarını kapsıyor sayılmaz; nested compound output kendi reservation'ını kullanır. Üst evaluator allocation alt yazıcıları kapsar. Dosyanın silinmesi, byte'ların gerçekten serbest kalmasıyla aynı olay değildir: open mapping veya NTFS resident stream nedeniyle free-space kanıtı yetersizse FREE_PENDING charged kalır. Bütün mutable metadata/fence lifecycle ve tüm writer profillerinin toplam kabulü yalnız bu broker'ın varlığıyla kapanmaz.

FREE_PENDING için exact member ve free-space tabanı durable intent olarak saklanır; yeniden açılışta ve kapasite kabulünde tekrar ölçülür. Silme ve yeterli fiziksel alan dönüşü birlikte kanıtlanmadan charge düşmez. Retained seed, fiziksel kimliğin yanında canonical root yoluna bağlanır; Linux'ta silinmiş başka bir dizinin inode'u tekrar kullanıldığında eski receipt yeni dizine uygulanmaz. Receipt/WAL/checkpoint kontrol alanı için ayrı metadata admission vardır.

## Görevler, provider akışı ve keşif

```sh
viber goal-queue TASK --store /private/store --json
viber goal-queue TASK --store /private/store --command-file goal.json --json
viber attach TASK --store /private/store --provider-deltas --follow --json
viber check-discover --root /source/project --json
viber repo-graph --root /source/go-project --goos linux --goarch amd64 --cgo DISABLED --tags-complete --changed internal/app.go --json
viber repo-history --root /source/project --allow-local-history --cutoff 2026-10-10T00:00:00Z --target src/app.go --json
```

Goal queue prompt steering'den ayrıdır; sabit child kimliği, parent document/source binding ve durable activation intent tutar. Child ayrı görevdir; parent içeriğinden türeyen kopyalar privacy lineage'a yazılır. Ayrıntılar: [goal/stream sözleşmesi](SUPERVISOR_QUEUE_STREAM.md).

Yeni görevlerde parent work budget creation anında journal'a bağlanır. Her child için step, tool-call, input/output token ve active-time sınırlarının en fazla dörtte biri, mevcut boş kapasiteyle sınırlı ayrı bir allocation tutulur. Parent'ın kullanımı, in-flight rezervasyonu ve bütün child allocation'ları aynı havuzdan düşer; child tam parent bütçesinin yeni kopyasını alamaz. Yalnız henüz aktive edilmemiş QUEUED goal iptali allocation'ı bırakır. ACTIVATING/ACTIVATED/UNKNOWN allocation'ları konservatif olarak tutulur. ChatGPT child admission, modelin tam çıktı rezervasyonunu karşılamıyorsa reddedilir; wire cap küçültülmüş gibi davranılmaz.

Eski sürümde allocation kaydı olmadan oluşturulan child'lar `status`, `diff` ve historical inspection ile okunabilir; yeni dispatch/retry/activation yapamazlar ve `UNSUPPORTED_CAPABILITY` döner. Yeni child işi, kaynak parent'ın yeni ve açıkça admitted goal'ünden başlatılmalıdır. Scheduled child üzerinde `new-attempt` ile ek tam bütçe üretilemez. Bu uyumluluk sınırı eski kayıtları silmez veya bütçeyi otomatik sıfırlamaz.

`repo-graph` varsayılan olarak yalnız yakalanmış byte'lardan bounded parser, import ve declaration hint'leri çıkarır; `go/types` çalıştırmaz, coverage'ı PARTIAL işaretler ve check seçimini genişletir. `--cooperative-types` açıkça seçilirse host'ta source-only `go/types` çalışır, ancak eşzamanlı type checker için hard CPU/RSS sınırı yoktur. Build hedefi, tag'ler ve cgo açık analiz girdileridir; gerçek test ortamı ölçümü değildir. `--changed` öneri üretir, zorunlu check'leri kaldırmaz. Aynı model aracı `fs_graph`, policy read scope içindeki byte'lara bounded sayfalarla bakar. Varsayılan akış yalnız parse/import hint'i verir. Type-resolved kenarlar için `run --go-graph-runtime` ile operatörün ayrı tuttuğu manifestte immutable Docker image digest'i veya `docker image inspect --format '{{.Id}}'` çıktısı olan `sha256:<64hex>` image ID'si pinlenir. [Helper Dockerfile](../tools/go-graph-helper/Dockerfile), Go 1.27.2 toolchain image'inden sabit komutlu scratch image üretir; `.dockerignore` build context'inden cache, toolchain ve credential klasörlerini çıkarır. Docker job pinned builder digest'i kullanıp helper image ID'siyle bounded source-only integration testini çalıştırır. Yardımcı protokolündeki `typecheck_mode`, yapılan analiz kipini adlandırır; hard limit kanıtı değildir. Hard sınırlar yalnız operator-pinned profile ile owned runner/container/quiescence receipt doğrulandığında kabul edilir. Kaynak kodu yürütülmez, host import/compiler/LSP çalıştırılmaz ve graph hiçbir zaman check ya da verification verdict'ü değildir.

Provider delta cursor'u task event cursor'u değildir. Geçici ring'de kayıp varsa explicit gap/current state bildirilir; `--resync` bilinçli ilerletir. Delta, kalite/completion kanıtı değildir.

Check discovery framework/config/test/import ipuçları çıkarır, dependency closure'u yetkili protected scopes ile karşılaştırır. Repository kodunu çalıştırmaz ve yeni doğrulama yetkisi vermez. History cutoff'tan sonraki commitleri dışlayan bounded first-parent path metadata'sıdır; commit mesajı/kimliği çıkarılmaz. Loose objects ve bounded v2 index'li v2/v3 pack dosyaları native okunur; REF/OFS delta, object hash, pack/index checksum, CRC, boundary ve derinlik/byte sınırları kontrol edilir. Pack inventory değişirse eski rapor reddedilir. Desteklenmeyen format, alternate/shallow/graft/replace, limit aşımı veya eksik nesne PARTIAL/UNKNOWN kalır. Local embedding için `--semantic-query`, `--allow-local-embedding`, `--embedding-model`, `--embedding-revision` açıkça seçilir. Loopback endpoint'in cloud proxy olmadığı bağımsız attestation ile kanıtlanmış sayılmaz.

## Operatör kısıtlamaları ve deney kaydı

`run --operator-policy ABSOLUTE_PATH` source dışında, operatör tarafından seçilmiş bounded JSON'a bağlanır. Schema/revision, privacy restrictions ve optional allowed_effects digest'i task'ta tutulur. Dosya kaybı, aynı revision değişikliği veya yeni revision yeni işlemleri reddeder; aktif inference iptal edilir. Tarihsel replay erişimi korunur. Yeniden yetkilendirme yeni bound task gerektirir. Bu local profile centrally authenticated enterprise admin policy değildir.

```sh
viber eval-register --store /private/store --protocol protocol.json --dataset /frozen/dataset --assets assets.json --output /new/registration --json
viber eval-registration-inspect --registration /new/registration --json
viber eval-candidate TASK --store /private/store --recipe hidden.json --output /new/result --registration /new/registration --arm baseline --repeat 1 --json
```

Dataset asset inventory, condition/build/source/recipe ve single-use STARTED kaydı dispatch öncesi bağlanır. Interrupted assignment başarılı tekrar diye sunulmaz. Inspector sonucu owner'ın retained kayıtlarından üretir. Post-hoc candidate raporu preregistered hale gelmez. Bağımsız model cohort, holdout/pilot, reference p95 ve adoption kabulü ayrı kalır; [eval kılavuzu](EVALUATION.md).

## Güvenli teslim

`apply TASK --store PATH --output NEW_PATH` güncel üç yönlü merge ve per-file preflight paketi üretir. Varsayılan yol live source yazmaz; `target_written=false`, `UNSUPPORTED_STRONG_EXCLUSIVITY` veya conflict bildirir.

| Kaynak/platform | Live apply/restore profili | Kabul sınırı |
|---|---|---|
| Windows fixed NTFS / yayımlanan `5e6a006ff6c0` | `apply --exclusive-ntfs` ile opt-in; exact owned checkpoint üzerinden restore ve read-only `delivery-reconcile`. | Exact-source Windows hostile-writer/checkpoint CI geçti. Mevcut single-link regular dosya içerikleriyle sınırlı profil; genel dosya oluşturma/silme yetkisi değildir. |
| Current main / Linux veya macOS | Doğrudan live apply/restore unsupported; fresh review package ve patch üretilebilir. | Advisory lock/hash/path kontrolü hostile writer'ı durdurmadığından fail-closed; PRD §13.3'ün candidate/patch fallback'idir, tek başına koşulsuz PRD açığı değildir. |
| Yayımlanmış eski `engineering-f1b323d…` paket | Yeni main live-delivery komut/profili yok; yayımlanmış export akışını kullanın. | Main'deki değişiklik ve kabul eski public binary'ye aktarılmaz. |

Windows profili yalnız yerel fixed NTFS üzerinde, önceden var olan 1–256 regular, tek linkli dosyanın içerik değişimini kapsar; dosya başına 64 MiB ve toplam before+after 256 MiB sınırı vardır. Yeni/silinen dosya, izin değişimi, symlink/reparse, açık writer/mapped view veya exclusive acquisition hatası kabul edilmez. Herhangi bir platformda varsayılan apply live source yazmaz:

```powershell
.\viber.exe apply TASK --store STORE --output NEW_REVIEW_DIRECTORY --exclusive-ntfs --json
.\viber.exe restore TASK --store STORE --checkpoint APPLIED_OPERATION_ID --json
.\viber.exe delivery-reconcile TASK --store STORE --checkpoint UNKNOWN_OPERATION_ID --json
```

Current operator effect allowlist'i kullanılıyorsa apply için `workspace.apply`, restore için `workspace.restore` izni gerekir; `export` tek başına kaynak yazı yetkisi vermez. Kayıtlı plan ve before/after bytes yazıdan önce tutulur; per-file synced readback ve aggregate receipt tamamlanmadan başarı verilmez. Restore exact APPLIED operation kimliğini ister; sonradan düzenlenmiş veya kimliği değişmiş dosyayı reddeder. İlgisiz dosyaları geri almaz.

Kesintide UNKNOWN kaydı kalır. `delivery-reconcile` hedefe yazmaz; bütün touched dosyalar exact postimage ve kimlikleriyle yeniden exclusively alınabiliyorsa receipt'i tamamlar. Mixed/preimage/user-edit durumu UNKNOWN kalır; otomatik retry veya force restore yapılmaz. Touched-file receipt bütün workspace'i VERIFIED yapmaz. Linux/macOS'ta genel live apply/restore backend'i unsupported'tır; review/export paketindeki patch'i kullanıcı kendi çalışma biçimiyle inceler ve uygular.

## Ayrı izinli yerel eğitim export'u

```sh
viber training-export TASK --store /private/store --output /new/training --consent consent.json --samples reviewed-samples.json --json
```

Önce görevin kullanıcı config'inde `restrictions.training_export=true` seçilir. Project config izin veremez; project veya current operator reddi kullanıcı opt-in'ini daraltır. Telemetry izni eğitim izni değildir. Export ağa bağlanmaz, provider çağırmaz ve otomatik ham trajectory/context toplamaz.

Samples schema 1, en fazla 256 adet `SFT`, `PREFERENCE`, `PROCESS`, `CONTEXT`, `PLANNER` veya `ROUTER` kaydı içerir. Her kayıt `input`, `target` ve yalnız preference için `rejected` JSON'udur. Toplam 1 MiB, her alan 64 KiB ile sınırlıdır. Kullanıcı exact dosyanın içeriğini ve kaynak kullanım haklarını inceler. Heuristic tarama keyfi JSON'un bütün sırlarını bulma garantisi vermediği için otomatik secret-safe etiketi üretilmez; yalnız bilinçli incelenmiş içeriği dışa aktarın.

Consent schema 1; `task_id`, `task_seq`, `document_digest`, `candidate_digest`, exact samples bytes SHA-256 olan `samples_digest`, `license`, `provenance_digest`, en fazla 24 saat ileride UTC `expires_at` ve `acknowledgment="I_REVIEWED_EXACT_PAYLOAD_AND_LICENSE_FOR_LOCAL_TRAINING_EXPORT"` alanlarını içerir. Güncel task/document/source/payload eşleşmiyorsa output yaratılmaz. İzin modeli config opt-in ve bu exact-payload consent'in ikisini de gerektirir. Lisans/provenance operatör beyanıdır; bağımsız doğrulama değildir.

Manifest kaliteyi kaynak task'tan kaydeder; başarılı check veya kullanıcı incelemesinden gold label çıkarmaz. Samples ve manifest fiziksel kotaya ve managed privacy lineage'a kaydolur. Task deletion ve retention purge bu yerel türevi de kapsar. Başka yere elle kopyalanmış içerik üzerinde yönetilen silme iddiası yoktur.

## Source-bound sıralama eğitimi

```sh
viber rank-policy init --registry /private/ranking --json
viber rank-policy train --registry /private/ranking --id project-search --version 1 --license MIT --dataset dev.json --dataset-sha EXACT_DEV_FILE_SHA --json
viber rank-policy evaluate --registry /private/ranking --definition reviewed-definition.json --dataset holdout.json --dataset-sha EXACT_HOLDOUT_FILE_SHA --json
viber rank-policy status --registry /private/ranking --json
```

Train, DEV etiketlerinde 16 bounded BM25/trigram ağırlık çiftini gerçekten çalıştırır. Sonuçtaki definition JSON'unu ayrı dosyaya kaydedip inceleyin. Evaluate, aynı source/protocol'e bağlı bağımsız HOLDOUT sorgularını gerçekten çalıştırır; DEV sorgusunun normalize edilmiş eşini, farklı ağırlık beyanını ve tekrarlanan evaluation assignment'ı reddeder. Tekil immutable receipt, executable/source/dataset digest'leri ve MRR/latency ölçümleri saklanır. Etiketler operatörün incelenmiş beyanıdır; bağımsız ürün kalite kabulü değildir.

`select` exact definition/evaluation SHA ile SHADOW, CANARY veya ACTIVE seçer. ACTIVE açık `I_REVIEWED_HELDOUT_LABELS_AND_APPROVE_ADVISORY_RANKING_ONLY` onayı ister. `disable` kill switch'tir; eski exact receipt yeniden seçilerek rollback yapılır. Kullanım `run --ranking-registry /private/ranking` ile açıkça etkinleştirilir. Öğrenilen ağırlıklar policy, kaynak kapsamı, literal arama, doğrulama veya yazı yetkisini değiştirmez. Cursor mevcut seçim revision'ına bağlanır; seçim değişmişse hazırlanmış sayfa verilmez.

## Sürümlü danışman skill'ler

`skills source|propose|evaluate|activate|rollback|revoke|expire|status|load` komutları ayrı registry ve trusted evaluator public key policy'siyle çalışır. Propose exact içerik/lisansın dış registry'ye bilinçli kopyalanmasını gerektirir; evaluate yalnız güvenilen imzalı, source/version/protocol'e bağlı receipt kabul eder. Task, project ve ayrı onaylı generic scope desteklenir. `run --skills-registry DIR --skills-trust-policy FILE` verilmezse kapalıdır. Model `skill_advice` ile mevcut scope'taki exact version'ı okuyabilir; eski kaynak, revoked skill veya değişmiş trust revision reddedilir. Skill metni araç/verification yetkisi vermez. Registry operatörün ayrı yönettiği kopyadır; task deletion başka yerdeki bu açıkça alınmış kopyayı sildiğini iddia etmez.

## Yeni repository üretimi

`repo-blueprint` exact source capture ve incelenmiş blueprint'ten immutable plan üretir. `repo-generate` bu planın digest'ine ve exact dosya bytes JSON'una bağlanarak yalnız yeni ve kaynak/store ile ayrık dizine yayımlar. Path alias, symlink, existing target ve değişmiş source reddedilir. Hook, generated code veya dependency kurulumunu host'ta çalıştırmaz. Protected check config verilirse check origin bağlanır; check çalıştırılmadan PASS üretilmez. Publication sonrası validation binding hatası oluşursa yayımlanan dizinin receipt'i ve `validation_error` birlikte gösterilir.

## Onaylı MCP extension

`run --extension-manifest ABSOLUTE_PATH` operatörün kaynak/store dışında tuttuğu canonical manifest'e bağlanır. Manifest digest'i görevde sabitlenir; değişmiş dosya aynı göreve sessizce yüklenmez. Manifest, exact server/tool/schema, image digest ve process sınırlarını içerir. Model `mcp_…` araçlarını görebilir; her çağrı `EXTENSION_CALL` isteğiyle ayrıca kullanıcı onayı bekler. `/requests`, `/respond REQUEST_ID approve`, `/resume` akışı kullanılır.

Server pinned Docker profiliyle read-only kaynak ve network-none izolasyonunda başlatılır. Initialize, exact tools/list ve tek tools/call kontrol edilir; sınırlı cevap ve transcript provenance tutulur. Cevap test/geçerlik kanıtı değildir. Kaynak kapsamı, güncel policy, operation digest ve process lease çağrıdan önce doğrulanır. Kesilen dispatch UNKNOWN ve charged kalır; dış manifest sonradan değişse bile retained intent cleanup için yeterlidir. Bu profil ağ erişimi veya yazı yapan extension yetkisi vermez. Actual Docker kabulü yalnız CI engine gerçekten çalıştırdığında PASS sayılır.
