# Viber

Viber, kodlama görevlerini kullanıcı dosyalarını koruyarak, izole candidate'lar ve kalıcı işlem kayıtları üzerinden yürütmek için geliştirilen yerel bir agent harness'ıdır. Hedef ürün; model önerilerini yetki, bütçe, kaynak bütünlüğü ve bağımsız doğrulama kontrollerinden geçirir.

**Mevcut sürüm: 0.9.0-dev — mühendislik önizlemesi. Üretim sürümü değildir.** CLI fixture, yerel Ollama veya ChatGPT aboneliğiyle aynı kalıcı native tool döngüsünü çalıştırır. ChatGPT girişi resmî Sign in with ChatGPT akışını kullanır; API anahtarı gerekmez. Auth/streaming kodu offline güvenlik testlerinden geçti; gerçek hesap kabulü henüz yapılmadı. OpenAI/Anthropic API-key CLI bağlantısı da vardır; gerçek endpoint kabulü beklemektedir. Kullanıcının kaynak bağlı incelemesi ve korunan V4 STDIO senaryoları için bağımsız doğrulama vardır; genel test-framework doğrulaması ve üretim kabulü tamamlanmamıştır.

[Ürün gereksinimleri](prd.md) · [0.6 PRD durum analizi](docs/PRD_STATUS_ANALYSIS.md) · [Anahtarsız yürütme planı](docs/KEYLESS_EXECUTION_PLAN.md) · [Tamamlama planı](docs/IMPLEMENTATION_PLAN.md) · [Kabul kapıları](docs/RELEASE_GATES.md) · [Doğrulama kaydı](docs/VALIDATION.md)

## Bugün ne yapabilirsiniz?

- Anahtar gerektirmeyen deterministik fixture ile görev açabilir, dosya okuyabilir, candidate değişikliği ve diff üretebilirsiniz.
- ChatGPT hesabıyla giriş yapabilir, hesap seçebilir ve izinli abonelik modeliyle görev başlatabilirsiniz.
- Kayıtlı check çalıştırabilir; exact çıktı, context ve analysis raporunu inceleyebilirsiniz.
- Source-bound FTS5/BM25/trigram ranked aramayı ve BUILD/TEST/ERROR/DIAGNOSTIC log seçimini native araçlarla kullanabilirsiniz. [Kullanım, sınırlar ve benchmark](docs/CONTEXT_RETRIEVAL.md).
- Dondurulmuş terminal adayı ayrı private owner ile gizli STDIO testine alabilir; kernel beyanı ve bağımsız sonucu ayrı görebilirsiniz. [Değerlendirme kullanımı](docs/EVALUATION.md).
- Önceden tanımlı JSONL ölçümlerini çözüm/kontrol kohortları, tekrarlar, eksik maliyetler ve eşleştirilmiş repo aralıklarıyla raporlayabilirsiniz.
- Terminal görevden intent ve toplam token hesabını koruyan yeni deneme oluşturabilirsiniz.
- Candidate yazısını `review` modunda inceleyip tek işleme bağlı onay verebilirsiniz.
- Durumu, bütçeyi, bekleyen istekleri, geçmiş olayları ve replay sonucunu inceleyebilirsiniz.
- Nonterminal görevleri pause/cancel/resume ile kontrol edebilir; ham steering mesajını kalıcı kaydedebilirsiniz.
- Exact before/after byte'ları ve patch içeren teslim paketi çıkarabilir; store'u yedekleyip yeni dizine geri yükleyebilirsiniz.
- Mevcut format 1 store'larını açık migration komutuyla format 2'ye yükseltebilirsiniz.

Fixture demosu kaynak dosyasını değiştirmez. Değişiklik ayrı candidate'da tutulur. Varsayılan fixture profili `VERIFIED` üretmez: beklenen demo sonucu `FINISHED / UNVERIFIED / SATISFIED` ve açık doğrulama yükümlülüğüdür.

TUI, background owner, detach/attach, durable queue, compaction, kaynak hesapları ve orphan GC aşağıda anlatılır. Canlı workspace apply/restore, genel güçlü test-framework doğrulaması, tam retention/delete, semantic AST/LSP ve imzalı kurulum paketleri henüz hazır değildir. Güncel teslim ve açık işler [anahtarsız ilerleme kaydında](docs/KEYLESS_PROGRESS.md); ayrıntılı 0.6 değerlendirmesi [önceki durum analizinde](docs/PRD_STATUS_ANALYSIS.md).

## Gereksinimler ve platform durumu

| Bileşen | Gereksinim |
|---|---|
| Derleme | `go.mod` ile eşleşen Go 1.27.2 |
| Repo'yu indirme | Git |
| Normal fixture kullanımı | Model anahtarı ve Docker gerekmez |
| Git-aware capture | `--git`; desteklenen index/repo biçimi. Capture host Git executable'ını çalıştırmaz |
| İzole process geliştirme testi | Önceden indirilmiş, digest ile sabitlenmiş Linux Docker image'ı ve Linux engine |
| Store | Kaynak kökün dışında, yerel ve kullanıcı erişimiyle korunan dizin |

Windows native CLI ve Linux CLI/Unix IPC üzerinde yerel test kanıtı vardır. Go 1.27.2 foundation commit'i üç native CI platformunda (Windows/Linux/macOS) test, migration ve güvenlik kapılarını geçti; Linux race/fuzz ve gerçek Docker işi de yeşildir. [Foundation CI](https://github.com/ixayldz/Viber/actions/runs/37955831284). Bu sonuç Windows native untrusted process sandbox kabulü değildir. Docker broker sınırlı bir geliştirme profilidir. Kullanıcının sonraki onayıyla geçici privileged engine üzerinde gerçek rootful restart/fencing geçti; bu hosttaki rootless profil gerekli kaynak controller'ları olmadığı için reddedildi. [Güncel kabul kaydı](docs/VALIDATION.md) ve [PRD teslim planı](docs/KEYLESS_DELIVERY_PLAN.md).

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

Task bittikten sonra kaynakta bağımsız kullanıcı editleri varsa B/C/U üç yönlü preview alın:

~~~powershell
.\bin\viber.exe delivery-preview greeting --store $store --output (Join-Path $demoBase 'merged-preview') --json
~~~

READY çıktısında `merged/` tam birleşmiş candidate'ı, `changes.patch` güncel kaynak → merged farkını taşır. Çakışmada path kararları çıkar, kısmi merged dosyası yayımlanmaz. Canlı kaynak ve Git index değişmez; merged sonuç `UNVERIFIED` olarak yeniden doğrulama ister. [Kullanım, binding, conflict ve sınırlar](docs/DELIVERY_PREVIEW.md).

Parent kayıtlı check runtime'ı içeriyorsa `delivery-reverify` reviewed manifest'ten yeni immutable `ANALYSIS` task'ı oluşturur; `resume` fresh native check'leri çalıştırır. Static local scheduler API anahtarı istemez. Eski PASS/onay/goal review taşınmaz; source/protected closure değişmişse import reddedilir. [Komut dosyası, retry ve kalite sınırı](docs/DELIVERY_PREVIEW.md#birleşmiş-candidateı-tekrar-doğrulama).

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
| 0 | FINISHED + VERIFIED + SATISFIED ve açık zorunlu yükümlülük yok; varsayılan fixture profili bunu üretmez; kaynak bağlı operator review + bağımsız V4 gözlemci için kullanılabilir |
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

Mevcut backup sınırları: 512 MiB toplam, 256 MiB DB, 32.768 dosya. Current backup schema 2 bağımsız private silme authority'sini ve mevcut watermark'ı gerektirir. Eski watermark, eksik authority ve kayıt dışı kopya restore edilemez. Eski schema 1 backup manifest'i normal restore akışına kabul edilmez; migration mevcut legacy store üzerinde bound recovery doğrulaması yapar. Farklı makine/path rebinding ve imzalı backup kapsamı yoktur. [Görev içeriği silme kılavuzu](docs/PRIVACY_DELETION.md) kapsamı ve retry akışını açıklar; otomatik retention ayrı geliştirmedir.

## Eski store'u yükseltme

Yeni store format 2 kullanır. Format 1 kendiliğinden yükseltilmez. Eski store'da bütün task'lar terminal olmalı, açık UNKNOWN effect/reservation bulunmamalı ve aktif owner durmalıdır.

~~~powershell
.\bin\viber.exe store-migration-status --store OLD_STORE --json
.\bin\viber.exe store-migrate --store OLD_STORE --backup-output NEW_BACKUP_PATH --command-id upgrade-001 --json
.\bin\viber.exe store-migration-status --store OLD_STORE --json
~~~

`OLD_STORE`/`NEW_BACKUP_PATH` gerçek yollarla değiştirilmelidir. Migration öncesi backup **fresh restore ile doğrulanır**. Yarım migration read-only recovery bırakır; receipt/backup bağını koruyarak aynı command ID ve aynı backup yolu ile uzlaştırılır. Unsupported downgrade mevcut store'u değiştirmez. Dizin/marker silerek veya yeni ID ile bu korumaları atlamayın.

[ADR 0006](docs/adr/0006-store-migration.md) tarihsel kapsamı kaydeder. [Migration demo script'i](scripts/migration-demo.ps1) mevcut legacy store'u açık `-AllowInPlaceMigration` seçimiyle yükseltir; eski backup'ı aktif bir store'a restore etmez.

## Anahtarsız yerel model: Ollama (0.7)

Ön koşul: Bu makinede Ollama ve tool calling destekleyen, önceden indirilmiş gerçekten local bir model bulunmalı. Endpoint cloud proxy olmamalı. Bu çalışma ortamında kurulu Ollama gözlenmedi; actual model kabulü henüz yapılmadı. Loopback HTTP kabul testleri gerçek model kalitesi kanıtı değildir.

~~~powershell
.\bin\viber.exe run "Kaynak kodunu incele ve izole candidate üzerinde değişiklik öner" --provider ollama --endpoint http://127.0.0.1:11434 --model INSTALLED_LOCAL_MODEL --local-model --context-limit 32768 --output-limit 512 --model-timeout-ms 120000 --root SOURCE_ROOT --store STORE_OUTSIDE_SOURCE --task local-work --autonomy review --allow-unverified --json
~~~

`INSTALLED_LOCAL_MODEL`, `SOURCE_ROOT` ve `STORE_OUTSIDE_SOURCE` gerçek değerlerle değiştirilmelidir. `--local-model` model/endpoint'in yerel çalıştığına dair operator beyanıdır; teknik egress proof değildir. Literal `127.0.0.1`/`::1` kullanılmalı; `localhost`, remote origin, credential, cloud model biçimleri ve fixture/local flag karışımı reddedilir.

Context kapasitesini kurulu modelin doğrulanmış sınırına göre seçin. `num_ctx`/`num_predict` request'e bağlanır; mevcut preflight conservative byte üst sınırıdır, provider tokenizer conformance değildir. Zorunlu içerik sığmazsa çağrıdan önce WAITING_RESOURCE olur. [Ollama chat API](https://docs.ollama.com/api/chat) ve [context yapılandırması](https://docs.ollama.com/context-length).

Review onayları fixture örneğindeki gibi `requests/respond/resume` ile verilir. Model gerçek tool çağrıları üretse de source doğrudan değişmez; varsayılan sonuç UNVERIFIED kalır; aşağıdaki kaynak bağlı V4 observer yolu ayrıdır. Usage kaybı, timeout/transport belirsizliği veya bildirilen model uyuşmazlığı BLOCKED ve korunmuş reservation bırakır; kör tekrar yoktur. Ollama için --stream NDJSON yolu vardır; thinking profili henüz desteklenmez. Sıradan kayıtlı Docker check computation evidence verir. Bağımsız V4 observer, fixed case discovery ve reviewed coverage ile ayrıca yapılandırılır.

## Native dependency planı (0.7)

Model `plan_propose`, `plan_next`, `plan_finish` araçlarını kullanabilir. Plan current spec/policy/candidate'a bağlanır; DEPENDS_ON cycle/unknown criterion/check/unsafe scope reddedilir. Scheduler tek aktif node tutar. Active node'un read/write contract'ı native araçlara ek restriction olarak uygulanır.

Node output'u immutable `MODEL_AUTHORED_UNREVIEWED` work-product artifact'ıdır. `IMPLEMENTED` durumunun anlamı plan çıktısının kaydıdır; PASS/VERIFIED değildir. Tamamlanmamış plan sınırlı final teslimi de durdurur. Scope revision eski planı current kullanımdan çıkarır; tarihsel belge/artifact korunur. Plan `status`/IPC/history/backup/fresh restore ile taşınır. Paralel workers ve store'lar arası mutex bu sürümde açılmaz.

## Store genelinde token bütçesi (0.7)

Yeni store'un ilk görevi, değişmez ortak input/output token limitlerini kaydeder. Varsayılan sınırlar 67.108.864 input ve 4.194.304 output token'dır; daha küçük değerler ilk `run` sırasında `--store-input-tokens N --store-output-tokens N` ile seçilebilir. Sonraki görevler aynı sınırları devralır; mevcut sınırları bu flag'lerle değiştirmek reddedilir. Görev başına step/tool/time/token sınırları ayrıca uygulanır.

~~~powershell
.\bin\viber.exe budget greeting --store $store --json
~~~

`charged` gözlenen kullanım, `reserved` henüz sonuçlanmamış üst sınırdır. Her model intent'i ve rezervasyonu aynı SQLite transaction'ında commit edilir. Kapasite yetmezse model dispatch edilmeden `WAITING_RESOURCE / GLOBAL_TOKEN_BUDGET_EXHAUSTED` oluşur. Aynı receipt ikinci kez charge edilmez. Bildirilen gerçek overage kayda alınır ve sonraki çalışma durur; unknown risk cancel/restart/restore ile silinmez.

`status`/`inspect` ayrıca görevdeki reservation/request/profile/raw-response bağlarını `state.token_account` içinde gösterir. Fixture kullanımının kaynağı `FIXTURE_REPORTED`, gerçek adapter'ınki `PROVIDER_REPORTED`'dır; bunlar bağımsız kalite veya parasal fatura kanıtı değildir. İptal ve kurtarma komutları token çalışma limitine tabi değildir. 0.9 resource ledger ve fiziksel disk reserve akışı aşağıda anlatılır.

0.6 ve daha eski görevleri içeren store `LEGACY_UNTRACKED` olarak görünür. Eski görevler kendi mevcut sözleşmeleriyle okunur/resume edilir; tarihsel kullanım bilinmeden yeni global hesabın dışında bırakılmaz. 0.7'de yeni görevler için yeni bir store kullanın. Yeni task/account alanları eski strict decoder tarafından yorumlanamaz; downlevel resume desteklenmez. Mevcut SQLite şema 2 korunur, örtük migration yapılmaz.

## Model ve API anahtarı durumu

| Profil | Anahtar | Bugün kullanıcı akışı |
|---|---|---|
| Offline fixture | Gerekmez | Çalışır; deterministik, gerçek inference yok |
| OpenAI Responses, API hesabı | Mevcut adapter yolunda API credential gerekir | CLI runtime bağlı; gerçek endpoint kabulü eksik |
| Anthropic Messages | Mevcut adapter yolunda API key gerekir | CLI runtime bağlı; gerçek endpoint kabulü eksik |
| Ollama, gerçekten yerel model | Yerel API için gerekmez | CLI runtime bağlantısı var; gerçek kurulu model kabulü ve production conformance eksik |
| ChatGPT coding aboneliği | API anahtarı gerekmez; browser OAuth gerekir | Auth ve stateless SSE runtime bağlı; gerçek hesap kabulü bekler |
| Diğer coding abonelikleri | Provider belirler | Auth sağlayıcısı şu an yalnız chatgpt |
| Ollama cloud | Cloud credential gerekir | Mevcut yerel profil desteklemez |

`OPENAI_API_KEY` ve `ANTHROPIC_API_KEY` seçilen process environment’ında yapılandırılmalıdır; .env otomatik okunmaz. API provider’ları ayrıca `--allow-remote` ister. Credential değeri task/context/output’a kaydedilmez; metadata yalnız fixed environment handle adını taşır. ChatGPT aboneliği ayrı auth yoludur ve bu API anahtarlarını kullanmaz. Anahtarları fixture/repository/response dosyasına yazmayın.

OpenAI ve Anthropic'in alternatif kimlik doğrulama seçenekleri de vardır; bu repo onların workload identity yaşam döngüsünü uygulamaz. Ollama'da local API üzerinden bir cloud model çağrısı da uzak inference olabilir; yalnız endpoint'in loopback olması yeterli kabul değildir. Resmi kaynaklar: [OpenAI authentication](https://developers.openai.com/api/reference/overview/#authentication), [Anthropic authentication](https://platform.claude.com/docs/en/manage-claude/authentication), [Ollama authentication](https://docs.ollama.com/api/authentication). Model gerektiren ve gerektirmeyen backlog [analizde](docs/PRD_STATUS_ANALYSIS.md) ayrı verilmiştir.

## Veri ve güvenlik sınırları

Görev store'u ham kullanıcı niyeti, captured kaynak byte'ları, model/fixture yanıtları, context manifest'leri, geçmiş belgeler ve artifact'lar tutar. Store'u seçilen source root'un dışına koyun. Demo `.cache` altında güvenlidir çünkü seçilen source yalnız `examples/offline/source` dizinidir; kendi projenizin tamamı source ise store'u o projenin dışında seçin.

Snapshot varsayılan olarak en çok 10.000 dosya, dosya başına 2 MiB ve toplam 32 MiB captured içerikle sınırlıdır. Binary baseline ve desteklenmeyen path/index türleri dışlanır veya reddedilir; excluded dosya için yokluk iddia edilmez. İki scan eşitliği `BEST_EFFORT` capture'dır; atomik filesystem snapshot değildir.

Policy/path/hash kontrolü gerçek OS sandbox'ıyla aynı güvenceyi vermez. `sandbox-run` ayrı operator profile/policy/authority ve pinned image gerektiren advanced geliştirme yüzeyidir. Model keyfi shell komutu gönderemez. --check-runtime seçilirse yalnız --check-plan içindeki operator argv/image/profile çalışır. Protected `--check-plan` origin'i mutation öncesi bağlar ve check weakening'i sınırlar; Ordinary check sonucu UNVERIFIED kalır; ayrıca kayıtlı protected V4 STDIO observer ve kaynak bağlı goal review sınırlı candidate-only verification receipt üretebilir.

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
| Terminal task resume hatası | Terminal task_seq ile attempt --new-task ID --parent-seq N kullanın |
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
| `internal/auth` | SIWC giriş, credential koruma, hesap/refresh/revoke |
| `internal/owner` / `ipc` | Tek owner ve yerel komut iletişimi |

Gözlenmiş test sonuçları ve açık üretim kabulü [VALIDATION.md](docs/VALIDATION.md) içinde; gereksinim bazında puanlama [PRD_STATUS_ANALYSIS.md](docs/PRD_STATUS_ANALYSIS.md) içindedir. Bu README mevcut komutları anlatır; gelecekteki PRD komutları kullanım örneği diye sunulmaz.

## ChatGPT coding aboneliğiyle giriş (0.8)

API anahtarı yerine resmî [Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source/sign-in) kullanılır. Sistem tarayıcısında hesabınıza girin ve callback tamamlanınca terminale dönün:

~~~powershell
.\bin\viber.exe auth login --provider chatgpt --json
.\bin\viber.exe auth status --json
.\bin\viber.exe auth profiles --json
.\bin\viber.exe auth models --json
~~~

`chatgpt_plan_usage:true` gerekli granted scopes ve credential bulunduğunu gösterir. Yalnız kimlik girişi model kullanım izni değildir. Başlangıç SIWC kapsamı uygun Plus/Pro hesaplarıdır; kullanılabilirliği ve limitleri OpenAI belirler. Ticari/hosted entegrasyon ayrı partner koşullarına tabidir.

`auth models` güncel görünür hesabın kataloğunu getirir. Şu an runtime’da yalnız yayımlanmış output tavanı kayıtlı `gpt-6.1-sol` profili açıktır. Diğer coding aboneliklerinin girişi bu sürümde desteklenmez.

Yeni hesap, seçim ve kayıtlı hesaba yeniden giriş:

~~~powershell
.\bin\viber.exe auth login --json
.\bin\viber.exe auth select --profile SAVED_PROFILE_ID --json
.\bin\viber.exe auth login --profile SAVED_PROFILE_ID --json
~~~

`SAVED_PROFILE_ID` auth profiles çıktısındaki ID’dir. Görev profile bağlanır; sonradan auth select eski görevin hesabını değiştirmez.

~~~powershell
.\bin\viber.exe run "Kaynak kodunu incele ve izole candidate üzerinde düzeltme öner" --provider chatgpt --allow-remote --model gpt-6.1-sol --root SOURCE_ROOT --store STORE_OUTSIDE_SOURCE --task chatgpt-work --autonomy review --allow-unverified --json
~~~

Placeholder’ları gerçek yollarla değiştirin. `--allow-remote` context’in seçili provider’a gönderilmesine açık izindir. Review yine requests/respond/resume kullanır. Abonelik girişi güçlü verification yetkisi değildir.

Credential kullanıcı config dizininin `viber/auth` altındadır; `--auth-dir` hem source hem task store dışında olmalıdır. Windows kullanıcı DPAPI + private ACL kullanır. Linux/macOS credential dosyası AES-256-GCM ile şifrelenir; 32-byte anahtar Secret Service veya Keychain'de kalır, dosya owner-only/0600'dür. Vault yoksa plaintext fallback yapılmaz; eski plaintext formatı yalnız explicit migration ile dönüştürülür. Unix helper sabit system executable ve dar environment allowlist'i kullanır; loader/proxy/shell değişkenleri aktarılmaz. Credential dosyaları normal task akışıyla journal/context/export/backup’a kopyalanmaz; status token dökmez. Default source capture `credentials.bin`, `auth.lock` ve `.auth-*` adlarını her dizin seviyesinde dışlar; Git’te tracked olmaları filtreyi aşmaz. Bu dosyaları başka adlarla source’a veya prompt’a kopyalamayın; genel secret keşfi/derived privacy lineage henüz tamamlanmadı.

~~~powershell
.\bin\viber.exe auth logout --profile SAVED_PROFILE_ID --json
~~~

Local token’lar temizlenir. `remote_revocation_confirmed:false` sunucudaki iptalin doğrulanmadığını belirtir; [kullanım ayarlarını](https://chatgpt.com/settings/usage) kontrol edin.

SIWC store:false/stream:true ve viber tool namespace kullanır. Preview max_output_tokens kabul etmediğinden çağrı başına [modelin](https://developers.openai.com/api/docs/models/gpt-6.1-sol) yayımlanmış 128.000 output token tavanı rezerve edilir. Bu yolun toplam görev output bütçesi varsayılan 1.048.576’dır; `--max-output-tokens` toplam work limitidir, wire cap değildir. `--output-limit` Ollama/API runtime için wire cap’tir; ChatGPT yolunda kullanılamaz. response.completed gelmeden araç çalışmaz; kesik stream UNKNOWN rezervasyonu bırakır. [Resmî inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference), [preview sınırları](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).

## Context, analysis ve yeni deneme (0.8)

İlk offline demo’daki `$store` ile:

~~~powershell
.\bin\viber.exe context-why greeting --store $store --json
.\bin\viber.exe context-page greeting --store $store --offset 0 --limit 4096 --json
.\bin\viber.exe report greeting --store $store --json
~~~

Context why dahil edilme nedenlerini ve audit bağını gösterir. Component boyları bağımsız tokenizer ölçümü değildir. Context page saklanan canonical request’in exact base64 byte sayfasıdır; next_offset ile devam edilir. Arbitrary blob/host dosya lookup yoktur.

~~~powershell
$analysisStore = Join-Path $demoBase 'analysis-store'
.\bin\viber.exe run "Yakalanmış hello.txt dosyasını incele" --offline --fixture examples/offline/analysis.json --task-kind ANALYSIS --root examples/offline/source --store $analysisStore --task analysis --allow-unverified --json
.\bin\viber.exe report analysis --store $analysisStore --json
~~~

ANALYSIS candidate mutation’ını reddeder. Report inputs/spec/candidate’a bağlı immutable artifact’tır; MODEL_AUTHORED_UNREVIEWED independent goal review değildir. CODE görevinde değişiklik yoksa kind NO_CHANGES olur; export final artifact’ı taşır.

`--max-repairs` 0..8, varsayılan 2’dir. Eksik plan/check veya geçersiz final özetinde aynı amaç/bütçe içinde bounded repair devam eder. Kriterler yeniden yazılmaz; limit dolunca WAITING_USER olur.

~~~powershell
$parent = .\bin\viber.exe status greeting --store $store --json | ConvertFrom-Json
.\bin\viber.exe attempt greeting --store $store --new-task greeting-retry --parent-seq $parent.state.task_seq --fixture examples/offline/greeting.json --allow-unverified --json
.\bin\viber.exe resume greeting-retry --store $store --json
~~~

Yeni task ham intent/criteria’yı korur, canlı source’u yeniden yakalar. Eski plan/check/approval/quality taşınmaz; toplam charge sıfırlanmaz. Aynı new-task/payload kayıp creation cevabını uzlaştırır; farklı payload conflict olur. UNKNOWN parent effect yeni denemeyi engeller. Local/ChatGPT parent için fixture verilmez; runtime/profile korunur.

## Kayıtlı check ve lexical outline (0.8)

Model yalnız kayıtlı check ID gönderir; argv/image/network/profile seçemez. Linux Docker engine ve önceden kurulu digest-pinned image gerekir; otomatik pull veya host fallback yoktur.

~~~powershell
$checkStore = Join-Path $demoBase 'check-store'
.\bin\viber.exe run "Yakalanmış greeting için kayıtlı kontrolü çalıştır" --offline --fixture examples/offline/check.json --check-plan examples/offline/check-plan.json --check-runtime examples/offline/check-runtime.json --root examples/offline/source --store $checkStore --task check-demo --allow-unverified --json
.\bin\viber.exe checks check-demo --store $checkStore --json
.\bin\viber.exe check-output check-demo --store $checkStore --run-id run-greeting-check --stream stdout --offset 0 --limit 4096 --json
~~~

Image exact digest’i check-runtime.json içindedir. `check-config --runtime FILE --plan FILE --json` profile digest ve plan eşleşmesini process başlatmadan gösterir. Profile değişirse plan runner_digest de Viber canonical metadata digest’iyle eşleşmelidir. Test/helper/config closure’ını eksiksiz seçmek gerekir; explicit closure full discovery değildir.

Process nonroot/network-none/readonly source/root ve bounded CPU/memory/PID/scratch/output kullanır. Receipt candidate/spec/policy/origin’e bağlıdır. Candidate değişince current:false olur. PASS çıktısı veya exit 0 computation sonucudur; verification:UNKNOWN kalır.

`fs_outline` JS/TS/Python lexical declaration sayfaları, comment/string dışlama, exact source digest/span ve candidate/policy cursor bağları verir. AST/reference/LSP garantisi değildir.

## API hesabıyla OpenAI / Anthropic (0.8)

İlgili API anahtarını bu terminalin environment’ına güvenli şekilde yapılandırdıktan sonra provider/model/context profilini açıkça seçin:

~~~powershell
.\bin\viber.exe run "TASK" --provider openai --allow-remote --model MODEL_AVAILABLE_TO_API_ACCOUNT --context-limit 32768 --output-limit 2048 --root SOURCE_ROOT --store STORE_OUTSIDE_SOURCE --autonomy review --json
.\bin\viber.exe run "TASK" --provider anthropic --allow-remote --model MODEL_AVAILABLE_TO_API_ACCOUNT --context-limit 32768 --output-limit 2048 --root SOURCE_ROOT --store STORE_OUTSIDE_SOURCE --autonomy review --json
~~~

API model ID, context kapasitesi ve output limiti hesabınızdaki doğrulanmış profil olmalıdır; bunlar operator beyanıdır, tüm modeller için conformance garantisi değildir. API endpoint sabittir; custom origin/proxy/redirect veya izinsiz provider fallback açılmaz. OpenAI/Anthropic için varsayılan JSON response’dur; `--stream` ile sırasıyla Responses SSE ve Messages SSE seçilir. Ollama’da aynı flag NDJSON kullanır. Stream seçimi task/runtime/request/profile’a bağlanır ve restore’da doğrulanır; provider bunu sessizce değiştiremez. SIWC her zaman SSE kullanır ve ayrıca stream flag’i istemez.

Credential/policy inference transport’u çağrılmadan reddedilirse kernel `KERNEL_NO_DISPATCH` receipt’iyle rezervasyonu sıfır kullanımla settle eder ve WAITING_RESOURCE olur. Credential düzeltildikten sonra explicit resume mümkündür. Gönderilmiş/kesilmiş request bu receipt’i alamaz; UNKNOWN reservation korunur. Bu ayrım billing veya kalite doğrulaması değildir.

Gerçek API kabulü bu çalışma kapsamında anahtar kullanılmadan test edilmedi; offline protocol/admission/privacy kanıtı vardır.

Stream, UI’ya canlı delta yayını değildir: 8 MiB’ye kadar private wire cevap saklanır, ancak tam terminal protokol sınırı doğrulandıktan sonra native tools’a geçilir. Anthropic JSON argüman parçaları/signed thinking ve cumulative/cache usage; OpenAI terminal response; Ollama complete structured tool chunks korunur. Kesik/bozuk/sırasız/different-model/server-tool cevap authority vermez. JSON/stream mode, opaque continuation ve UNKNOWN usage için silent fallback/retry yoktur. Per-response tool batch en fazla 32 çağrıdır. [OpenAI Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses), [Anthropic Messages streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), [Ollama streaming](https://docs.ollama.com/capabilities/streaming).

## Context devamlılığı ve model geçişi (0.9)

`continuity-info` current task sequence, profile digest, pin ve compaction referanslarını verir. `context-edit` yalnız settled, nonterminal ve input barrier olmayan sınırda çalışır. Aynı command ID ile aynı JSON'u tekrar göndermek aynı receipt'i verir; içeriği değiştirmek reddedilir.

~~~powershell
.\bin\viber.exe continuity-info TASK --store STORE --json
.\bin\viber.exe context-edit TASK --store STORE --command-file compact.json --json
~~~

`compact.json`, çıktıda görülen sequence ile hazırlanır:

~~~json
{"command_id":"compact-001","task_id":"TASK","expected_task_seq":42,"action":"compact","keep_recent":8}
~~~

Sıkıştırma eski tam assistant/tool bloklarını immutable bir history archive'a taşır. Goal, raw intent, spec, policy, plan, budget, candidate ve check kayıtları değişmez. Bounded preview tarihsel ve güvenilmeyen gözlemdir. Zorunlu context sığmıyorsa çağrı yapılmaz; yeterli settled history varsa aynı mekanizma dispatch öncesi otomatik denenir.

`history-page TASK --store STORE --history-digest DIGEST --offset 0 --limit 4096 --json` exact retained canonical source baytlarını base64 döndürür. Modelin `history_page` aracı opaque provider continuation alanlarını çıkaran ayrı canonical projection kullanır; source ve projection digest'leri açıkça ayrıdır. Sadece o görevin kayıtlı archive referansları okunabilir.

Pin komutu, mevcut candidate'ın exact source digest ve byte aralığına bağlanır. `pin` nesnesi `id/path/candidate/source_digest/start/end` içerir; baytları kernel doldurur. En çok 16 pin, tek pin 16 KiB, toplam 64 KiB. `{"action":"unpin","pin_id":"ID",...}` açık kaldırma ister. Pin tarihsel candidate'a bağlı kalabilir; current evidence veya yetki sayılmaz.

`model-switch TASK --store STORE --command-file switch.json --json` komutunun alanları `command_id/task_id/expected_task_seq/expected_profile/runtime`'dır. `runtime` mevcut runtime nesnesinin, istenen model/capacity/output/timeout değerleri değiştirilmiş kopyasıdır. İlk profilin provider, endpoint, account, credential handle ve privacy izinleri korunur. Eski opaque continuation atılır; canonical text/tool pairing kalır. Eski token/resource charges kendi immutable profilinde tutulur. Yeni request context, kalan task/global token ve kaynak bütçesine sığmadan değişiklik commit edilmez. Pending/UNKNOWN işlemde geçiş reddedilir.

## Kaynak bütçesi ve fiziksel disk rezervi (0.9)

Yeni store'un ilk görevi immutable `--resource-policy policy.json` ile para, CPU allocation, disk work, active operation time ve eşzamanlı child limitlerini seçebilir. Sonraki görevler aynı policy'yi devralır. Varsayılan para limiti sıfırdır; local/fixture ve ChatGPT plan kullanımının API çağrısı maliyeti bu ledger'da sıfırdır. Abonelik, elektrik, local GPU veya gerçek cloud faturası ölçülmez.

~~~powershell
.\bin\viber.exe resources TASK --store STORE --json
~~~

Çıktı `ledger` ve `disk` nesnelerini verir. Money birimi currency'nin milyonda biridir. CPU/disk charge ölçülen fiziksel tüketim olarak sunulmaz: admit edilen konservatif allocation üst sınırı charge edilir; operation time ayrı tutulur. Model token/resource intent ve settlement aynı metadata transaction'ındadır. Docker child reservation, profilin PID üst sınırını kapsar; UNKNOWN native süreçte rezerv silinmez.

Ücretli OpenAI/Anthropic modelleri explicit versioned operator price catalog ister. Fiyatlar otomatik çekilmez; aşağıdaki miktarlar yalnız sentetik örnektir:

~~~json
{
  "schema_version":1,
  "currency":"USD",
  "limits":{"money_micros":10000000,"cpu_millis":68719476736,"disk_bytes":8589934592,"wall_millis":4294967296,"children":512},
  "operator_prices":[{"version":"operator-example-v1","provider":"openai","model":"SELECTED_MODEL","input_micros_per_million_tokens":1000000,"output_micros_per_million_tokens":1000000}]
}
~~~

Böyle bir task'a `--resource-policy policy.json` ekleyin. Rate alanları bir milyon token için micro currency miktarıdır; sonuç yukarı yuvarlanır. Catalog'da bulunmayan paid model başlatılmaz; model switch de catalog ve kalan bütçeyi kontrol eder.

Store, 32 MiB private `control.reserve` alanını random bytes + sync ile gerçekten ayırır ve OS allocation bilgisini doğrular. 64 MiB free-space low watermark altında yeni work yazısı engellenir. Sınırlı control/receipt/checkpoint yazıları rezervi tüketebilir; kapasite tükenirse son durable checkpoint korunarak yazı reddedilir. Work tekrar başlamadan rezerv doldurulur. Bu mekanizma network filesystem veya bütün güç kesintisi senaryoları için conformance iddiası değildir.

İlk privacy authority kurulurken dizinin fiziksel kimliği reserve/genesis yazılmadan önce owner journal'ına pending allocation olarak kaydedilir. Açılış yarıda kalırsa sonraki açılış aynı dizin ve rezervle devam eder; yeni 32 MiB kopyalar yaratmaz. Kayıp/değiştirilmiş dizin, farklı genesis veya yabancı dosya varsa işlem durur. Authority dizinini elle taşımak/silmek desteklenen recovery değildir. [ADR 0016](docs/adr/0016-privacy-authority-allocation.md) crash sınırlarını, [detaylı PRD planı](docs/KEYLESS_DELIVERY_PLAN.md) kalan işlerin kabul koşullarını açıklar.

Privacy metadata kapasitesini bağımsız CLI'dan veya background owner üzerinden inceleyebilirsiniz:

```powershell
.\bin\viber.exe privacy-capacity --store C:\ViberData\review --json
.\bin\viber.exe privacy-capacity --store C:\ViberData\review --replenish-control-reserve --json
.\bin\viber.exe privacy-capacity --store C:\ViberData\review --retire-operation-leases --json
```

TUI karşılığı `/capacity`, `/capacity replenish` ve `/capacity retire-operations`'tır. `work_blockers` sabit reason code'larıyla journal, metadata byte, owner katalog, düşük disk alanı ve eksik/tükenmiş kontrol reserve'ini ayırır. `supported_next_actions` çalışan refresh/replenish ve inactive operation lease retirement eylemlerini gösterir. Replenish en fazla 32 MiB reserve'i gerçek allocation/readback ile tamamlar; low watermark yetmiyorsa reddeder. Restore operation lease'leri normal kapanışta temizlenir; abrupt death sonrasında sonraki restore admission veya `--retire-operation-leases` yalnız current authority'ye bağlı typed ve kilitsiz lease'leri geri alır. Aktif kilitler, owner scope'ları, eski untyped lease'ler ve process fence'leri tutulur. Farklı authority, foreign/replaced/hardlinked dosya temizlenmez; bakım güvenle reddedilir. İki bakım flag'i aynı komutta kullanılamaz. Task içeriği, deletion watermark ve ledger değişmez. Bu iş journal checkpoint veya persistent owner retirement yerine geçmez; dosyaları elle prune etmeyin. `observation_only:true` ve `work_probe_bytes:4096` anlık küçük work gözlemini ifade eder; gelecekteki büyük allocation/deletion izni değildir.

0.8 ve önceki resource-untracked store'lara yeni accounted task karıştırılmaz. Yeni 0.9 görevleri için fresh store kullanın; mevcut tarihsel accounting'e sıfır kullanım eklenmez. SQLite format 2 korunur; yeni optional task alanlarını eski strict clients okuyamaz, downlevel resume desteklenmez.

## UNKNOWN model riskini hesapta kapatma (0.9)

UNKNOWN model response/usage kendiliğinden retry edilmez. Kullanıcı `status/resources` içindeki exact reservation/request/profile ve current task sequence'i inceleyip `reconcile-model-risk TASK --store STORE --command-file risk.json --json` verebilir:

~~~json
{"command_id":"risk-001","task_id":"TASK","expected_task_seq":42,"reservation_id":"RESERVATION_ID","request_digest":"REQUEST_SHA256","profile_digest":"PROFILE_SHA256","decision":"ACCOUNT_FULL_UPPER_BOUND_WITHOUT_OUTPUT"}
~~~

Bu karar **bütün üst sınırı harcama olarak kaydeder**. Usage source `OPERATOR_ASSUMED_UPPER_BOUND`'dır; provider-reported usage değildir. Kayıp response kullanılmaz, candidate/goal değişmez, quality yükselmez. Sonraki inference ayrı yeni çağrıdır; kullanılan bütçe geri verilmez. Cancel edilmiş task hesaplanabilir fakat resume edilemez. Native process belirsizliği bu komutla çözülemez; gerçek process quiescence gerekir.

## 0.9: background kullanım, bakım ve güvenli destek

Yeni task'ı arka planda başlatmak için normal run komutuna `--detach` ekleyin. Aynı binary ayrı bir owner süreci başlatır; JSON yanıttaki `invocation_id` tekrar çağrı ve uzlaşma için saklanır. Task store'u kaynak dizinin dışında kalmalıdır.

```powershell
.\bin\viber.exe run "Kaynağı incele" --offline --fixture examples/offline/analysis.json --root examples/offline/source --store C:\ViberData\review --task review --task-kind ANALYSIS --allow-unverified --detach --json
.\bin\viber.exe attach review --store C:\ViberData\review --after 0 --follow --json
.\bin\viber.exe owner-status --store C:\ViberData\review --json
.\bin\viber.exe pause review --store C:\ViberData\review --json
.\bin\viber.exe owner-stop --store C:\ViberData\review --json
```

`attach` çıktısı sürümlü JSONL sayfalarıdır; durable event'ler task sequence/event ID ile dedup edilir. Son başarılı sayfanın `cursor` değerini sonraki `--after` olarak kullanın. Yavaş/kapalı attach istemcisi detached task'ı durdurmaz. Geçici model text delta'ları retained journal değildir ve replay edilmez. Owner-stop aktif task'ı reddeder; başarıdan önce endpoint'in kapanmasını ve OS writer kilidinin bırakılmasını denetler. Owner süreci kaybolmuşsa eski admitted invocation otomatik tekrar çalıştırılmaz; task/reservation durumunu inceleyin. Bu dilim tam hostile-principal/orphan-child/backend fencing kabulünü kapatmaz.

Mevcut task için `detach TASK --store PATH --command-id ID --json` kullanabilirsiniz. `serve-background` owner'ı task dispatch etmeden başlatır. Aynı ID ile aynı aktif veya tamamlanmış invocation yeniden dispatch edilmez.

GC önce review edilebilir plan üretir:

```powershell
$plan = .\bin\viber.exe store-gc-preview --store C:\ViberData\review --json | ConvertFrom-Json
$gcCommand = @{ command_id = "gc-20261009-1"; plan = $plan } | ConvertTo-Json -Depth 100
[IO.File]::WriteAllText((Join-Path (Get-Location) "gc-command.json"), $gcCommand, [Text.UTF8Encoding]::new($false))
.\bin\viber.exe store-gc --store C:\ViberData\review --command-file gc-command.json --json
```

GC, tam journal/historical-document/source/receipt grafiğinde erişilemeyen immutable CAS nesnelerini kaldırır. Standalone snapshot-save scope'ları korunur; pending/UNKNOWN effect veya aktif görev varsa işlem reddedilir. Plan journal/reference head'e bağlıdır; değişmiş head yeni preview gerektirir. Intent ve per-file receipts backup/fresh restore'da korunur. Bu komut task silme, sensitive deletion, managed backup purge veya fiziksel disk silme değildir; retained task verisini süreye göre otomatik silmez.

Paylaşılabilir destek bilgisi için:

```powershell
.\bin\viber.exe support --store C:\ViberData\review --output C:\ViberSupport\review-1 --json
```

`support.json` yalnız enum durumları, sayısal bütçe/sayaç ve provider sınıfı içerir. Prompt, kod, dosya adı/hash'i, task ID, model adı/endpoint, kullanıcı email/credential bilgisi ve ham log dahil edilmez; otomatik gönderim yapılmaz. Normal `export` ise kullanıcı tarafından istenen kod/patch çıktısıdır ve paylaşmadan önce içeriği incelenmelidir.

Unix auth storage artık OS vault bağlı AES-256-GCM kullanır: Linux'ta `/usr/bin/secret-tool` ve erişilebilir Secret Service, macOS'ta Keychain/`/usr/bin/security` gerekir. Vault kapalı/eksikse plaintext'e geri dönülmez. 0.8 Unix plaintext kayıtları için açık yerel geçiş:

```sh
viber auth migrate-storage --auth-dir /private/path/to/viber/auth --json
```

Bu işlem hesap/profile kimliğini korur; inference, registration veya refresh yapmaz ve plaintext backup üretmez. Windows kullanıcı DPAPI korunur. Credential vault'u task store/source/backup'tan ayrıdır; yalnız credentials.bin kopyalamak Unix vault anahtarını taşımaz. Offline encryption/helper-protocol fixture'ları gerçek desktop vault/unlock kabulü değildir; CI'da native macOS Keychain ve Linux Secret Service unique-item roundtrip/cleanup ayrıca geçti. Helper yalnız local Unix D-Bus IPC kullanır; remote/mixed/autolaunch address açıkça reddedilir. Executable ve bütün resolved parent dizinleri root-owned/nonwritable olmalıdır. Her çıktı pipe'ı 512 byte ile sınırlıdır; helper deadline sonrası pipe drain de sınırlıdır. Gürültülü başarılı key lookup, bozuk/noncanonical key veya locked/missing/invalid key ile mevcut credential rotasyonu reddedilir; eski ciphertext değiştirilmez. Bu hata kontratları ve gerçek subprocess pipe/timeout testleri CI'da zorunludur; bütün native locked/multi-user desktop davranışları için kabul değildir. Exact revision kanıtı [VALIDATION](docs/VALIDATION.md) içindedir.


## Terminal arayüzü, prompt kuyruğu ve config (0.9)

Bir görevi `run --detach` ile başlatıp terminal arayüzünü açabilirsiniz:

```powershell
.\bin\viber.exe ui TASK_ID --store C:\ViberData\review
# Erişilebilir veya pipe üzerinden kullanım:
.\bin\viber.exe ui TASK_ID --store C:\ViberData\review --line
```

`/help` mevcut komutları gösterir. `/status /diff /plan /pause /resume /cancel /model /queue /context /why /evidence /budget /trace /requests` görev API'lerine bağlıdır. `/read a.txt` canlı diskteki son sürümü değil, hash ile bağlı candidate'ın ilk 16 KiB sayfasını okur; kapsam ve devam offset'i görünür. Source/log metni terminal kontrol dizisi çalıştıramaz. `/quit` arayüzü ayırır; detached görev sürer.

Normal metin ham steering girdisi olarak kaydedilir ve görev pause edilir. Yeni scope açıkça `/revise` ile kabul edilir; offline görevde `/revise C:\fixtures\fresh.json` yeni fixture ister. Ardından `/resume` ayrı bir detached invocation başlatır. `/respond REQUEST_ID approve` veya `reject` yalnız o güncel karar isteğine cevap verir; resume otomatik değildir. İlk Ctrl+C pause, sonraki tamamlanmış kontrolün ardından Ctrl+C cancel ister. Cancel source restore yapmaz.

`/queue add Daha sonra @a.txt dosyasını incele` çalışan scope'u değiştirmeden kalıcı bir mesaj ekler. `/queue` kayıtları gösterir; `/queue activate QUEUE_ID` mesajı mevcut görevin steering bariyerine atomik aktarır; `/queue remove QUEUE_ID` sıradan çıkarır. Sıralı yeni task'ların otomatik başlatılması bu dilimde yoktur; terminal task için yeni attempt gerekir. Queue remove tarihsel payload privacy deletion değildir.

@file fuzzy referansı izinli candidate dosyaları üzerinde çözülür. Birden fazla eşleşme varsa exact captured path seçmeniz gerekir; boşluklu isim `@"folder/a file.txt"` şeklinde yazılabilir. Tab sınırlı eşleşme listesini gösterir. Reference candidate ve blob hash'ini taşır; yeni yetki veya kalite kanıtı üretmez.

Secretsiz global config varsayılan olarak OS user config dizinindeki `viber/config.json` dosyasıdır; `run --user-config FILE` başka bir dış dosya seçer. Proje config'i root'taki `viber.config.json` dosyasıdır. Kullanıcı → proje tercihleri → açık CLI değerleri uygulanır; güvenlik kısıtları kesişir. Örnek:

```json
{
  "schema_version": 1,
  "preferences": {"provider": "ollama", "model": "local-model", "autonomy": "guided"},
  "restrictions": {
    "remote_inference": false,
    "allowed_providers": ["ollama"],
    "embedding_egress": false,
    "reranker_egress": false,
    "tool_mcp_browser_egress": false,
    "telemetry_export": false,
    "training_export": false,
    "cross_project_memory": false,
    "sensitive_paths": ["private/**", "customer-notes.txt"]
  }
}
```

`allowed_providers: []` bütün provider'ları yasaklar; alanın bulunmaması veya null olması ek restriction koymaz. Proje remote=true deseydi bile user remote=false kararını açamazdı. Config dosyasında API anahtarı bulunmaz; bilinmeyen alan ve parse hataları içeriği echo etmeden reddedilir. Hassas source dosyaları CAS'a girmeden filtrelenir; project config'in ham içeriği de capture edilmez. Gerekli Git/ignore metadata'sı privacy scope tarafından yasaklanırsa `--git` fail-closed olur; directory capture kullanılabilir. Mevcut görevin config'i task'ta saklanır; bir config dosyasını sonradan değiştirmek aktif görevin yetkisini gizlice değiştirmez.

Bu arayüz 0.9 engineering dilimidir. Generic extension/MCP, full verifier/framework kapsamı, exclusive live apply/restore, tam lifecycle deletion ve beta/platform kabulü ayrı geliştirme gerektirir. [Yürütme planı](docs/KEYLESS_EXECUTION_PLAN.md) gerçek kapsamı, [ADR 0012](docs/adr/0012-queue-terminal-and-config.md) sınırları kaydeder.

## Bağımsız V4 davranış doğrulaması

Önceden kurulu pinned Linux Docker image'ıyla, model API anahtarı olmadan:

~~~powershell
./scripts/observer-demo.ps1
~~~

Demo private bir source/store açar, ayrı operator senaryolarını kaydeder, iki tekrar ve baseline karşılaştırması yapar. Kaynak değişmeden kalır. Beklenen sonuç bu belirli senaryolar için FINISHED / VERIFIED / SATISFIED ve exit 0'dır; release_ready yine false'tur. İlk offline patch demosunun exit 2 sözleşmesi aynıdır.

Kendi STDIO uygulamanız için recipe JSON'un plan ve runtime alanlarını tanımlayın. Runtime'da pinned profile ve observer_suites bulunur; suite schema_version=1, level=V4, protocol=KERNEL_STDIO_EXACT_V1, repeats=2..4 taşır. Her case benzersiz id, base64 input/expected_stdout/expected_stderr ve expected_exit_code taşır. Check kind=TEST, expected_tests exact case ID listesi, argv yalnız test edilen uygulama komutu ve closure gerçek bağımlılık kapsamıdır. Beklenen sonuçları değiştiren dosyaları source'a koymayın.

~~~powershell
viber check-prepare --file .\recipe.json --output .\checks.json
viber run "Ham hedef" --root .\project --store .\private-store --offline --fixture .\fixture.json --check-config .\checks.json --goal-review .\goal-review.json --json
viber verification TASK --store .\private-store --json
viber check-output TASK --store .\private-store --run-id RUN --case CASE --repeat 2 --scope baseline --offset 0 --limit 16384 --json
~~~

check-prepare yalnız canonical oracle/profile bağlarını oluşturur; process veya model çalıştırmaz, mevcut output'u değiştirmez. Eski check-config --runtime FILE --plan FILE --json komutu korunur. --check-config ile --check-plan/--check-runtime aynı görevde karıştırılmaz. Observer ve review dosyaları source kökünün dışında olmalıdır.

Goal review operator tarafından hazırlanır: schema_version=1, input_digests ham girdilerin SHA-256 değerleri, coverage her required requirement_id için check_ids, dependency_checks bütün protected check ID'leri ve acknowledgement=RAW_GOALS_AND_EXECUTION_DEPENDENCIES_REVIEWED_V1. Bu ifade, ham hedefin ve gerçek check bağımlılıklarının incelendiğine dair kullanıcı kabulüdür. Model veya proje config'i bunu kendiliğinden vermez. Yeni scope/raw input review'ı current olmaktan çıkarır.

Her deneme saklanır. Beklenmeyen çıktı, sıfır/eksik senaryo, stale digest, unknown cleanup ve başarısız tekrar PASS'e dönüşmez. Aynı candidate üzerinde sonradan PASS almak önceki FAIL'i silmez. Gizli fixture verisi stdout'a yazılabileceği için protected observer çıktısı model tool'una verilmez; yerel kullanıcı exact byte sayfalarını okuyabilir. /verification TUI'da aynı kaynak bağlı kriter tablosunu gösterir.

Bu V4 external STDIO case kabulüdür. Genel repository test runner'ının kendi “PASS” beyanı, arbitrary framework discovery, bütün V0–V5 katmanları, analysis raporunun semantik kabulü veya üretim release'i değildir. [Güven zinciri ve sınırlar](docs/adr/0013-protected-stdio-observer.md).

## Beklenmedik owner kapanışından sonra native işlem recovery

Yeni görevlerde Docker check'lerinin izinli subject kimlikleri process başlamadan kalıcı kaydedilir. Owner aniden ölürse resume, eski check'i körlemesine tekrar çalıştırmaz; işlem UNKNOWN olarak durur. Aynı physical store'u daha yeni owner generation ile açtıktan sonra durumu inceleyin:

```powershell
.\bin\viber.exe runtime-info TASK --store STORE --json
```

Background owner hâlâ eski generation'da çalışıyorsa önce `owner-stop --store STORE` ile kapatıp store'u yeniden açın. `runtime-info` içindeki `can_fence` true olduğunda `command_template` nesnesini dış bir JSON dosyasına kaydedin; `command_id` alanına benzersiz, sabit bir ID verin. Sonra:

```powershell
.\bin\viber.exe reconcile-native-risk TASK --store STORE --command-file native-recovery.json --json
```

Komut kayıp check çıktısını başarı saymaz. Eski subject'leri exact kimlikleriyle temizleyip gecikmiş create/start'ı engelleyen, hiç çalıştırılmayan fence container'larını tutar. Tam native kaynak üst sınırı charge edilir; task budget tükenebilir. Eksik kanıtta UNKNOWN rezervasyon kalır; aynı command ID effect'i yeniden çalıştırmaz. Yeni deneme için önce current cursor'u inceleyip yeni command ID kullanın. TUI'de `/runtime` aynı görünümü verir.

Fence container'larını otomatik prune etmeyin: eski owner'ın gecikmiş create isteğine karşı tutulan isim rezervasyonlarıdır. Restore edilen store kendi physical runtime kimliğini alır ve özgün store'un süreçlerine dokunamaz. Legacy unowned intent bu cleanup yolu ile serbest bırakılamaz. Tam backend/rootless/host process lifetime kabulü hâlâ geliştirme kapsamındadır; [ADR 0014](docs/adr/0014-owned-process-fences.md) ayrıntıları açıklar.
## Bağımsız değerlendirme

Anahtar ve model çağrısı gerektirmeyen raporlama için eval-report/eval-report-inspect; ayrı Docker owner ile gizli candidate testi için eval-candidate/eval-inspect kullanılır. [Adım adım kılavuz](docs/EVALUATION.md) protokol, fixture, maliyet, belirsizlik, çıkış kodu ve recovery sınırlarını anlatır. scripts/independent-eval-demo.ps1 görünür testten VERIFIED alan eksik uppercase uygulamasının gizli testte FAIL olarak kaydedildiğini, asıl görev ve source aynı kalırken gösterir. İçe aktarılan raporlar unattested metadata; native aday testleri post-hoc exact STDIO kapsamıdır. Yeni native evaluator ayrı owner ve benzersiz `evaluator_task` kimliğiyle original'ın external deletion authority'sine bağlıdır; original silinmeden önce evaluator ve kayıtlı report/backup/restore kopyaları silinir. Süreç TaskCreated öncesi çökerse parent silme manifest'i staged candidate'ı kapsar. Bu komutlar tek başına production veya harness superiority gate açmaz.
