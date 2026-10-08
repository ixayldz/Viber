# Viber

Viber, kodlama görevlerini kullanıcı dosyalarını koruyarak, izole candidate'lar ve kalıcı işlem kayıtları üzerinden yürütmek için geliştirilen yerel bir agent harness'ıdır. Hedef ürün; model önerilerini yetki, bütçe, kaynak bütünlüğü ve bağımsız doğrulama kontrollerinden geçirir.

**Mevcut sürüm: 0.6.0-dev — offline mühendislik önizlemesi. Üretim sürümü değildir.** Bugün CLI önceden tanımlanmış fixture yanıtlarını çalıştırır; serbest bir isteği gerçek bir LLM ile kodlayan son kullanıcı akışı henüz açılmamıştır. OpenAI, Anthropic ve Ollama protokol adapter'ları vardır; CLI'daki agent döngüsüne gerçek provider bağlantısı tamamlanmamıştır.

[Ürün gereksinimleri](prd.md) · [PRD durum analizi](docs/PRD_STATUS_ANALYSIS.md) · [Tamamlama planı](docs/IMPLEMENTATION_PLAN.md) · [Kabul kapıları](docs/RELEASE_GATES.md) · [Doğrulama kaydı](docs/VALIDATION.md)

## Bugün ne yapabilirsiniz?

- Anahtar gerektirmeyen deterministik fixture ile görev açabilir, dosya okuyabilir, candidate değişikliği ve diff üretebilirsiniz.
- Candidate yazısını `review` modunda inceleyip tek işleme bağlı onay verebilirsiniz.
- Durumu, bütçeyi, bekleyen istekleri, geçmiş olayları ve replay sonucunu inceleyebilirsiniz.
- Nonterminal görevleri pause/cancel/resume ile kontrol edebilir; ham steering mesajını kalıcı kaydedebilirsiniz.
- Exact before/after byte'ları ve patch içeren teslim paketi çıkarabilir; store'u yedekleyip yeni dizine geri yükleyebilirsiniz.
- Mevcut format 1 store'larını açık migration komutuyla format 2'ye yükseltebilirsiniz.

Fixture demosu kaynak dosyasını değiştirmez. Değişiklik ayrı candidate'da tutulur. Mevcut profil `VERIFIED` üretmez: beklenen demo sonucu `FINISHED / UNVERIFIED / SATISFIED` ve açık doğrulama yükümlülüğüdür.

Canlı workspace'e `apply`/`restore`, gerçek modelle genel kodlama, TUI, detach/attach, compaction, retention/delete, outline/symbol ve imzalı kurulum paketleri henüz hazır değildir. Ayrıntılar [mevcut durum analizinde](docs/PRD_STATUS_ANALYSIS.md).

## Gereksinimler ve platform durumu

| Bileşen | Gereksinim |
|---|---|
| Derleme | `go.mod` ile eşleşen Go 1.27.1 |
| Repo'yu indirme | Git |
| Normal fixture kullanımı | Model anahtarı ve Docker gerekmez |
| Git-aware capture | `--git`; desteklenen index/repo biçimi. Capture host Git executable'ını çalıştırmaz |
| İzole process geliştirme testi | Önceden indirilmiş, digest ile sabitlenmiş Linux Docker image'ı ve Linux engine |
| Store | Kaynak kökün dışında, yerel ve kullanıcı erişimiyle korunan dizin |

Windows native CLI ve Linux CLI/Unix IPC üzerinde yerel test kanıtı vardır. macOS/arm64 için çapraz derleme vardır; native macOS kabulü bu çalışma kapsamında gözlenmemiştir. Windows CLI çalışması, Windows'ta native untrusted process sandbox'ının üretime hazır olduğu anlamına gelmez. Docker broker da sınırlı bir geliştirme profilidir.

## Kurulum

Henüz yayımlanmış installer veya paket yöneticisi dağıtımı yoktur. Kaynaktan derleyin:

~~~sh
git clone https://github.com/ixayldz/Viber.git
cd Viber
go version
go mod download
~~~

Windows / PowerShell:

~~~powershell
New-Item -ItemType Directory -Path bin -Force | Out-Null
go build -trimpath -o bin/viber.exe ./cmd/viber
.\bin\viber.exe version
.\bin\viber.exe doctor --json
~~~

Linux veya macOS'ta kaynak derlemesi:

~~~sh
mkdir -p bin
go build -trimpath -o bin/viber ./cmd/viber
./bin/viber version
./bin/viber doctor --json
~~~

`doctor` çıktısında `release_ready: false` beklenir. Bu komut mevcut yetenek bildirimini verir; bütün sandbox/provider/platform kabul testlerini çalıştırmaz.

Repo'daki `.tools`, `.cache` ve `bin` dizinleri Git'e dahil değildir. Yeni clone'da Go'yu ayrıca kurmalısınız. İlk dependency indirmesi ağ ister; bağımlılıklar hazırlandıktan sonraki fixture çalışması model servisine bağlanmaz. Aşağıdaki Windows örnekleri repo kökünde çalıştırılır; binary PATH'e eklenmişse `viber` kısaltması kullanılabilir.

## İlk görev: offline demo

Bu fixture yalnız `examples/offline/source/hello.txt` dosyasının başlangıçtaki exact `hello` byte'larına uygundur. Final newline dahil kaynak byte'larını değiştirmeyin. Fixture prompt'u yorumlayan bir model değildir; prompt'u değiştirmek fixture'ın önceden belirlenmiş davranışını değiştirmez.

### Windows / PowerShell

Her çalıştırmada yeni bir demo dizini oluşturun:

~~~powershell
$demoBase = Join-Path (Get-Location) ('.cache/readme-demo-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $demoBase | Out-Null
$store = Join-Path $demoBase 'store'

.\bin\viber.exe run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store $store --task greeting --allow-unverified --json
$LASTEXITCODE
~~~

Son komutun değeri **2** olmalıdır. Bu, açıkça izin verdiğiniz sınırlı sonucun teslim edildiğini belirtir; üretim doğrulaması değildir.

~~~powershell
.\bin\viber.exe status greeting --store $store --json
.\bin\viber.exe diff greeting --store $store --json
Get-Content -LiteralPath examples/offline/source/hello.txt -Raw
~~~

Candidate `hello, Viber` ve final newline içerir; canlı kaynak `hello` olarak kalır. `status` içinde outcome, quality ve fulfillment ayrı alanlardır. `--allow-unverified` verilmezse sınırlı teslim için bağlı kullanıcı isteği oluşur ve invocation exit 3 ile bekler.

### Linux / Bash

~~~sh
demo_base="$(mktemp -d)"
store="$demo_base/store"
./bin/viber run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store "$store" --task greeting --allow-unverified --json
printf 'run exit: %s\n' "$?"
./bin/viber status greeting --store "$store" --json
./bin/viber diff greeting --store "$store" --json
~~~

Exit 2 beklenir; `set -e` kullanan script'te bu beklenen kodu açıkça ele alın. macOS'ta aynı komut yüzeyi derlenir, ancak native platform kabulü henüz doğrulanmamıştır.

Windows'ta alternatif tek komutluk fixture akışı:

~~~powershell
.\scripts\offline-demo.ps1
~~~

Script benzersiz `.cache` dizinlerinde run, export, backup, fresh restore ve replay akışını kontrol eder; beklenen exit 2'yi doğru ele alır.

## Değişikliği inceleme ve dışarı alma

İlk demo sonrasında:

~~~powershell
$delivery = Join-Path $demoBase 'delivery'
.\bin\viber.exe export greeting --store $store --output $delivery --json
Get-Content -LiteralPath (Join-Path $delivery 'changes.patch') -Raw
Get-Content -LiteralPath (Join-Path $delivery 'report.json') -Raw
Get-Content -LiteralPath (Join-Path $delivery 'manifest.json') -Raw
~~~

Paket exact before/after byte artifact'ları, `changes.patch`, `report.json` ve en son yayımlanan `manifest.json` içerir. Çıktı dizini yeni olmalı, üst dizin önceden var olmalıdır. Binary değişiklikte `patch_complete: false` olabilir; text patch bütün değişikliği temsil etmeyebilir.

Export kullanıcı tarafından istenen yerel bir teslim işlemidir; görevi `VERIFIED` yapmaz ve canlı workspace'e uygulamaz. Bu paket tam store yedeği veya training dataset'i değildir. İçerik kaynak kodu/duyarlı veri taşıyabilir; paylaşmadan önce inceleyin. Genel secret-safe redaction ve privacy deletion henüz tamamlanmamıştır.

## Review modu: değişiklik öncesi onay

Aynı demo store'unda yeni task açabilirsiniz:

~~~powershell
.\bin\viber.exe run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store $store --task review-demo --autonomy review --allow-unverified --json
.\bin\viber.exe requests review-demo --store $store --json
~~~

Run exit **3** verir: okuma yapılmış, candidate yazısı `CANDIDATE_WRITE` isteğinde beklemektedir. İsteğin `action` alanındaki path, read-set ve değişiklik byte'larını inceleyin. `--allow-unverified` yalnız sınırlı final teslim iznidir; review yazı onayını atlamaz.

İncelediğiniz güncel isteği onaylamak için:

~~~powershell
$requests = .\bin\viber.exe requests review-demo --store $store --json | ConvertFrom-Json
$request = $requests | Where-Object { $_.kind -eq 'CANDIDATE_WRITE' -and $_.status -eq 'PENDING' } | Select-Object -First 1
if (-not $request) { throw 'Güncel candidate yazı isteği bulunamadı.' }

$response = [ordered]@{
    command_id = 'approval-' + [guid]::NewGuid().ToString('N')
    task_id = $request.task_id
    request_id = $request.request_id
    attempt = $request.attempt
    expected_spec_version = $request.spec_version
    expected_policy_digest = $request.policy_digest
    action_digest = $request.action_digest
    response = @{ decision = 'approve' }
}
$responseFile = Join-Path $demoBase 'response.json'
[IO.File]::WriteAllText($responseFile, ($response | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))

.\bin\viber.exe respond review-demo --store $store --request $request.request_id --response-file $responseFile --json
.\bin\viber.exe resume review-demo --store $store --json
~~~

`respond` onayı kalıcı kaydeder; yürütme için `resume` gerekir. Bu fixture'da resume exit 2 ile sonuçlanır. Reddetmek için `decision` değerini `reject` yapın.

Onay task/attempt/spec/policy/candidate/action'a bağlı, 24 saat süreli ve tek işlemliktir. Sonraki proposal'a yayılmaz. Aynı command ID ve aynı payload kayıtlı sonucu döndürür; aynı ID ile farklı payload `COMMAND_ID_CONFLICT` olur. Eski/expired yanıt `STALE_REQUEST` olur; `resume` kendi başına onay değildir.

`guided` varsayılandır; `auto` da yalnız bu offline native candidate kapsamını kullanır. Autonomy seçimi host shell, credential, uzak ağ veya live-write yetkisi açmaz.

## Durum, geçmiş ve çıkış kodları

~~~powershell
.\bin\viber.exe status greeting --store $store --json
.\bin\viber.exe inspect greeting --store $store --at 1 --json
.\bin\viber.exe replay greeting --store $store --until 1 --json
.\bin\viber.exe events greeting --store $store --after 0 --limit 64 --json
~~~

`--at`/`--until`/`--after` **task_seq** kullanır; store-geneli sıra ile aynı değildir. Inspect/replay için 0 current state'i seçer. Event sayfalama limiti 1–256'dır. Replay dış etkileri yeniden çalıştırmaz; retained journal bütünlüğünü denetler.

`--json` komuta göre structured JSON sonucu veya event kaydı üretir. `events` sayfalı JSON yanıtıdır. Bütün komutları production canlı JSONL stream/reconnect protokolü olarak değerlendirmeyin; attach, retained-gap resync ve backpressure kabulü eksiktir.

| run/resume exit | Anlam |
|---|---|
| 0 | FINISHED + VERIFIED + SATISFIED ve açık zorunlu yükümlülük yok; mevcut offline profil bunu üretmez |
| 2 | FINISHED; strict success olmayan sınırlı final sonuç |
| 3 | Kullanıcı/kaynak/onay/block bekleniyor; task terminal failure değildir |
| 4 | Runtime/task hatası veya final quality FAILED |
| 5 | Budget exhausted |
| 130 | İptal veya interrupted invocation |

`status`/`doctor`/`export`/`respond` gibi kontrol komutlarında exit 0 yalnız komutun başarıyla cevap verdiği anlamına gelir.

## Pause, resume, cancel ve yerel owner

Bu komutlar **nonterminal** task'lar içindir. Yukarıdaki bitmiş `greeting` task'ını resume etmeyin; yeni task ID oluşturun. PRD'nin terminal task için yeni attempt akışı henüz uygulanmamıştır.

~~~powershell
.\bin\viber.exe pause TASK_ID --store $store --json
.\bin\viber.exe resume TASK_ID --store $store --command-id resume-001 --json
.\bin\viber.exe cancel TASK_ID --store $store --command-id cancel-001 --json
~~~

`TASK_ID` kendi nonterminal task'ınızın kimliğiyle değiştirilmelidir. İlk Ctrl+C foreground invocation'ını güvenli pause yoluna sokar; `PAUSING` ile `PAUSED` farklıdır. Cancel değişiklikleri canlı workspace'ten geri alma işlemi değildir.

Mevcut store için foreground owner:

~~~powershell
.\bin\viber.exe serve --store $store --json
~~~

Ayrı terminalde aynı store'un tam yolunu `--store` ile kullanın. Çalışan owner'a status, diff, pause, cancel, resume, history, requests/respond ve steering komutları yerel IPC üzerinden gider. Windows'ta SID kontrollü named pipe, Unix'te peer kimliği denetlenen socket vardır; public TCP değildir.

`serve` daemon/detach değildir; terminali kapatınca kalıcı supervisor olarak çalışmaya devam etmez. Export/backup/migration gibi doğrudan store erişen işlemler aktif owner varken `STORE_OWNED` alabilir. Bunlardan önce owner'ı durdurun.

Command response'u kaybolursa `UNKNOWN_OPERATION_OUTCOME` görülebilir. Aynı işi yeni ID ile kör tekrar etmeyin; status/events ile uzlaştırın. Belirsiz model usage/effect rezervasyonları restart'ta silinmez.

## Steering ve kapsam değişikliği

Nonterminal görevde:

~~~powershell
.\bin\viber.exe steer TASK_ID "Yeni talimatın exact ham metni" --store $store --command-id steering-001 --json
~~~

Input önce kalıcı yazılır, admission bariyeri oluşturulur ve aktif native loop pause edilir. Bu sürümde doğal dil yeni bir gerçek model planına otomatik çevrilmez. Explicit `revise --revision-file` current spec/epoch/candidate bağlarıyla fresh fixture gerektirir; eski onay/continuation geçersizleşir, candidate ve kullanılmış/reserved bütçe korunur. Pending input çözülmeden iş devam edemez.

Revision JSON ve güncel binding kuralları için [geliştirme rehberine](docs/DEVELOPMENT.md) bakın. Bu advanced akış, üretim prompt queue/replanning özelliği değildir.

## Store yedeği ve geri yükleme

Owner çalışmıyorken, ilk demo değişkenleriyle:

~~~powershell
$backup = Join-Path $demoBase 'backup'
$restored = Join-Path $demoBase 'restored'
.\bin\viber.exe store-backup --store $store --output $backup --json
.\bin\viber.exe store-restore --backup $backup --store $restored --json
.\bin\viber.exe status greeting --store $restored --json
.\bin\viber.exe replay greeting --store $restored --until 0 --json
~~~

Backup SQLite snapshot + gerekli blob closure + digest/size manifest'i içerir. Restore **yeni dizine** yapılır; mevcut store'u overwrite etmez ve task effect'lerini çalıştırmaz. Yalnız sqlite dosyasını kopyalamak yeterli yedek değildir.

Mevcut backup sınırları: 512 MiB toplam, 256 MiB DB, 32.768 dosya. Yedek/restore aynı makinede aynı source kimliği için tasarlanmıştır; farklı makineye otomatik path rebinding yoktur. Backup imzalı değildir. Retention/delete henüz yoktur: deletion watermark 0 ve `UNSUPPORTED_NO_DELETIONS` gerçek sınırlamayı gösterir.

## Eski store'u yükseltme

Yeni store format 2 kullanır. Format 1 kendiliğinden yükseltilmez. Eski store'da bütün task'lar terminal olmalı, açık UNKNOWN effect/reservation bulunmamalı ve aktif owner durmalıdır.

~~~powershell
.\bin\viber.exe store-migration-status --store OLD_STORE --json
.\bin\viber.exe store-migrate --store OLD_STORE --backup-output NEW_BACKUP_PATH --command-id upgrade-001 --json
.\bin\viber.exe store-migration-status --store OLD_STORE --json
~~~

`OLD_STORE`/`NEW_BACKUP_PATH` gerçek yollarla değiştirilmelidir. Migration öncesi backup **fresh restore ile doğrulanır**. Yarım migration read-only recovery bırakır; receipt/backup bağını koruyarak aynı command ID ve aynı backup yolu ile uzlaştırılır. Unsupported downgrade mevcut store'u değiştirmez. Dizin/marker silerek veya yeni ID ile bu korumaları atlamayın.

[ADR 0006](docs/adr/0006-store-migration.md) kapsamı ve [migration demo script'i](scripts/migration-demo.ps1) tam native örneği açıklar.

## Model ve API anahtarı durumu

| Profil | Anahtar | Bugün kullanıcı akışı |
|---|---|---|
| Offline fixture | Gerekmez | Çalışır; deterministik, gerçek inference yok |
| OpenAI Responses | Mevcut adapter yolunda API credential gerekir | HTTP/protokol kodu var; CLI bağlantısı ve gerçek kabul eksik |
| Anthropic Messages | Mevcut adapter yolunda API key gerekir | HTTP/protokol kodu var; CLI bağlantısı ve gerçek kabul eksik |
| Ollama, gerçekten yerel model | Yerel API için gerekmez | Loopback adapter fixture testleri var; gerçek yerel model kabulü ve CLI bağlantısı eksik |
| Ollama cloud | Cloud credential gerekir | Mevcut yerel profil desteklemez |

`OPENAI_API_KEY`/`ANTHROPIC_API_KEY` ortam değişkeni eklemek veya `.env` oluşturmak bugün CLI'yı gerçek modellerle çalıştırmaz. Provider/model config, credential onboarding/keychain ve runtime wiring henüz tamamlanmamıştır. Anahtarı fixture'a, repo'ya veya response dosyasına yazmayın.

OpenAI ve Anthropic'in alternatif kimlik doğrulama seçenekleri de vardır; bu repo onların workload identity yaşam döngüsünü uygulamaz. Ollama'da local API üzerinden bir cloud model çağrısı da uzak inference olabilir; yalnız endpoint'in loopback olması yeterli kabul değildir. Resmi kaynaklar: [OpenAI authentication](https://developers.openai.com/api/reference/overview/#authentication), [Anthropic authentication](https://platform.claude.com/docs/en/manage-claude/authentication), [Ollama authentication](https://docs.ollama.com/api/authentication). Model gerektiren ve gerektirmeyen backlog [analizde](docs/PRD_STATUS_ANALYSIS.md) ayrı verilmiştir.

## Veri ve güvenlik sınırları

Görev store'u ham kullanıcı niyeti, captured kaynak byte'ları, model/fixture yanıtları, context manifest'leri, geçmiş belgeler ve artifact'lar tutar. Store'u seçilen source root'un dışına koyun. Demo `.cache` altında güvenlidir çünkü seçilen source yalnız `examples/offline/source` dizinidir; kendi projenizin tamamı source ise store'u o projenin dışında seçin.

Snapshot varsayılan olarak en çok 10.000 dosya, dosya başına 2 MiB ve toplam 32 MiB captured içerikle sınırlıdır. Binary baseline ve desteklenmeyen path/index türleri dışlanır veya reddedilir; excluded dosya için yokluk iddia edilmez. İki scan eşitliği `BEST_EFFORT` capture'dır; atomik filesystem snapshot değildir.

Policy/path/hash kontrolü gerçek OS sandbox'ıyla aynı güvenceyi vermez. `sandbox-run` ayrı operator profile/policy/authority ve pinned image gerektiren advanced geliştirme yüzeyidir. Normal fixture loop keyfi shell/test çalıştırmaz. Protected `--check-plan` origin'i mutation öncesi bağlar ve check weakening'i sınırlar; henüz trusted test observer veya güçlü verification receipt üretmez.

Telemetry/training bildirimi varsayılan OFF'tur; geniş config/privacy/secret-safe export ve türev verileri silme sistemi henüz tamamlanmamıştır. Kaynak/backup/delivery dizinlerini Git'e eklemeyin.

## Sorun giderme

| Belirti | Yapılacak işlem |
|---|---|
| `run` exit 2 | Demoda beklenen UNVERIFIED sınırlı sonuç; quality alanını okuyun |
| `run` exit 3 | `status` ve `requests` ile gerekli onay/kaynak/block durumunu inceleyin |
| `STORE_OWNED` | Aktif owner'ı kullanın; backup/export/migration için owner'ı durdurun |
| `STALE_BASE` / source changed | Kaynak/read-set değişmiş; eski patch'i zorlamayın. Yeni task veya uygun fresh revision gerekir |
| `STALE_REQUEST` | Güncel isteği yeniden okuyun; eski bağlarla yanıt göndermeyin |
| `COMMAND_ID_CONFLICT` | Aynı ID farklı komut/payload için kullanılmış |
| Terminal task resume hatası | Yeni task ID kullanın; yeni attempt özelliği henüz yok |
| `CONTEXT_TOO_SMALL` | Zorunlu içerik sığmıyor; otomatik kırpma yapılmaz |
| UNKNOWN usage/effect | Reservation korunur; kör tekrar yerine uzlaştırma gerekir |
| Output dizini zaten var | Yeni çıktı path'i seçin; overwrite desteklenmez |
| `apply`/`attach`/`delete` unsupported | Özellik henüz uygulamada yok; flag ile açılmaz |

## Geliştirme ve doğrulama

Windows:

~~~powershell
.\scripts\check.ps1
.\scripts\offline-demo.ps1
~~~

Diğer ortamlarda temel kontroller:

~~~sh
go mod verify
go vet ./...
go test -count=1 ./...
go build -trimpath -o bin/viber ./cmd/viber
~~~

Linux race kontrolü `go test -race -count=1 ./...` ile yapılır. Docker kabul testi için image önceden hazırlanıp `VIBER_DOCKER_TEST_IMAGE` ile açıkça seçilir; profil ayrıntıları CI ve geliştirme rehberindedir. SKIP, fixture PASS ve cross-compile native production kabulü sayılmaz.

Kod yerleşimi:

| Dizin | Görev |
|---|---|
| `cmd/viber` / `internal/cli` | Binary ve komut yüzeyi |
| `internal/kernel` / `store` | State machine, journal, checkpoint, migration |
| `internal/agent` | Offline loop, requests, steering, context/budget bağları |
| `internal/workspace` / `artifact` / `delivery` | Capture, candidate/CAS, export |
| `internal/policy` / `runner` / `fileguard` | Admission, Docker broker, path/metadata korumaları |
| `internal/model` / `context` / `verify` | Provider protokolleri, preflight, kalite/origin kuralları |
| `internal/owner` / `ipc` | Tek owner ve yerel komut iletişimi |

Gözlenmiş test sonuçları ve açık üretim kabulü [VALIDATION.md](docs/VALIDATION.md) içinde; gereksinim bazında puanlama [PRD_STATUS_ANALYSIS.md](docs/PRD_STATUS_ANALYSIS.md) içindedir. Bu README mevcut komutları anlatır; gelecekteki PRD komutları kullanım örneği diye sunulmaz.
