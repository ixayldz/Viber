# Doğrulama kaydı

Tarih: 8 Ekim 2026. Ürün: 0.5.0-dev. Bu sonuçlar offline engineering profiline aittir; PRD A–D üretim kabulü değildir.

| Kontrol | Sonuç / kapsam |
|---|---|
| gofmt / mod verify / go vet | PASS; pinned module checksums, Windows native ve Linux |
| Windows test | 260 test/alt test PASS (tam tur); 2 host symlink yetkisi SKIP; 0 FAIL |
| Linux race tests | PASS; tüm paketler, linux/amd64 |
| Linux strict JSON fuzz | PASS; bu oturumdaki 0.3 turunda 23.046 execution, corpus 89; encoding code değişmedi |
| Linux Git index fuzz | PASS; bu oturumdaki 0.3 turunda 253.819 execution, corpus 13; index decoder değişmedi |
| Windows binary / offline demo | PASS; fixture→candidate→export→backup→fresh restore→historical replay |
| Linux binary / doctor | PASS; linux/amd64 |
| macOS tüm paket + CLI cross-build | PASS; darwin/arm64; test binary'leri de çapraz derlendi; /bin/true ile runtime yürütme atlandı, native macOS smoke yapılmadı |
| Gerçek Docker engineering tests | PASS; Windows host/Linux engine; readonly root/source, exact mount source/image/argv, private namespaces/bounded scratch, non-root, network-none, forged PASS+exit7, timeout child cleanup |
| Protected origin/closure | PASS; pre-mutation durable operator origin; file/tree/absence/helper/config weakening deny; guarded review/guided/auto; additive revision coverage; restart/backup/fresh restore; frozen candidate/check/env/policy bindings. Strong verifier yok |
| Native paging | PASS; binary candidate/CRLF/split Unicode exact reassembly, 520 files + empty directory/exclusions, 71 matches/long-line hydration, stale query/candidate/policy cursor |
| Compiled context | PASS; mandatory full protocol/spec/policy, overflow öncesi sıfır fixture call/reservation, durable manifest/request restore, missing request/altered manifest/unpaired protocol reddi |
| Yerel owner IPC | PASS; Windows named pipe ve Linux Unix peer kimliği/PID; ayrı CLI süreci, stale generation/PID, frame kotası, response kaybı, uzun Unix path private socket |
| Aktif kontrol ve steering | PASS; concurrent status/pause/cancel, Ctrl+C pause, command dedup, raw barrier, spec/epoch revision, eski onay reddi, input backup/restore |
| Provider adapters | PASS; offline OpenAI/Anthropic vectors ve loopback Ollama fixtures; gerçek inference yapılmadı |
| Gerçek Git patch interoperability | PASS; disposable temp dizininde CRLF, Unicode, no-final-newline, boş dosya, ekleme/silme |

Windows iki SKIP: workspace ve fileguard symlink oluşturma testleri; host yetkisi yok. Aynı fixture'lar Linux race turunda geçti. Hostile junction/namespace escape ailesi bu sonuçla kapanmaz.

Linux test image'i resmi golang:1.27.1-bookworm@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66. Bu tur trusted validation runner'ında ağ kapalı, cap-drop ALL, no-new-privileges, bounded CPU/memory/PID ve exec scratch kullanıldı; checkout mount'u build cache/binary çıktıları için yazılabilirdi. Gerçek untrusted Docker profilinin readonly/non-root/no-network kontrolleri Windows host/Linux engine fixture'ında ayrıca geçti. Test ortamı tam backend conformance değildir.

Davranış kanıtları:

- Immutable blob publication DB pointer'dan önce; collision overwrite yok. Task/case scope, eksik blob, candidate bytes/mod/manifest kurcalaması reddedilir.
- Native index v2/v3/v4/SHA-256/worktree capture host Git helper/config/filter çalıştırmaz. Dirty/staged/untracked/index korunur. Index/ignore/read-set/listing/absence değişimi proposal'ı invalidate eder.
- Mutation öncesi typed protected origin baseline/raw input/spec/logical source root ile bağlıdır. Operator planı sonradan değiştirmek, check/expected discovery azaltmak, absent/tree/config/helper eklemek veya origin'i kaldırmak mevcut authority değiştiremez. Explicit closure review/authorized check revision/trusted observer henüz yoktur.
- Raw intent, whole task document, response, bütçe ve candidate kalıcıdır. Unknown usage/admitted effect restart/restore'da retained; blind retry yok.
- Request exact action/spec/policy/candidate/expiry'ye bağlıdır. Resume onay değildir. One-shot approval ikinci proposal'a yayılmaz. Response command dedup ve payload conflict denetlenir.
- Resume admission/completion ve error receipt durable/idempotent; response kaybı tekrar dispatch yaratmaz. Eski pause komutu yeni invocation'ı kesmez.
- Ham steering önce blob sonra barrier olarak kaydedilir; bound revision başlangıç scope/protected origin/budget'ı korur, eski onayları geçersiz kılar; missing raw input fail-closed olur.
- Event/payload/projection/command/sequence transaction; crash/rollback injection, projection/hash chain/tail ve command-result corruption fail-closed olur.
- Snapshot backup CAS+SQLite closure'ını birleştirir; manifest son yayınlanır. Corrupt/missing/unfinished/traversal/duplicate/schema/watermark/journal/reference saldırıları target yayınlamaz.
- Fresh restore mevcut store/source'u değiştirmez; pending reservation/approval bağları korunur. Model/tool effect çalıştırılmaz.
- Historical inspect/replay gerçek task cursor'unu verir ve full retained journal denetler. Event paging task/global sequence'yi ayırır.
- Paging partial range'i explicit tutar; cursor current candidate/policy'ye bağlıdır. Excluded source/search kapsamı universal absence olarak sunulmaz.
- Offline compiled context bütün mandatory protocol/spec/policy'yi korur; overflow çalışma çağrısından önce WAITING_RESOURCE olur. Manifest/request blob closure restore sırasında denetlenir.
- Export exact before/after bytes + patch + report + manifest taşır. Binary text patch coverage açıkça false; source/task quality değişmez.
- Model VERIFIED, stdout PASS, exit0 ve limited-result approval güçlü kalite üretmez. Offline delivery exit2 ve açık required obligation taşır.
- Policy/epoch/generation/barrier intersection ve strict canonical JSON testleri korunur. Kontrol komutları boş/yanlış store'da DB başlatmaz.

go-winio v0.6.2 bağımlılığı eklendikten sonra govulncheck v1.8.0 yeniden çalıştırıldı: No vulnerabilities found. Bu sonuç bütün güvenlik risklerinin yokluğu anlamına gelmez.

Açık kabul: tam sandbox escape/egress/fencing/clock; real provider+streaming; semantic spec/goal review; protected check closure/observer; complete budget/control reserve; tam IPC hostile-principal/process fencing/supervisor/provider steering conformance; live apply/restore concurrency; retention/delete/tombstone/migration/OS power-loss; platform install/signature/SBOM, native macOS smoke ve independent benchmark/pilot. [Release kaydı](RELEASE_GATES.md) kapıları kapalı tutar.

Yerel kanıt dosyaları (Git'e girmez): .cache/windows-owner-tests.jsonl, .cache/windows-owner-final-tests.jsonl, .cache/linux-owner-validation.txt, .cache/linux-owner-final-validation.txt, .cache/owner-vulncheck.txt ve .cache/offline-owner-demo.jsonl. Yeni binary bin/viber.exe, Linux ve darwin/arm64 binary'leri .cache altındadır.
0.4 son doğrulama kayıtları: .cache/windows-context-tests.jsonl, .cache/linux-context-validation.txt ve .cache/offline-context-demo.jsonl. Windows binary 0.4.0-dev; son offline demo .cache/demo-e755db1ed48f45da8166bb0758453b81 altında. Önceki owner/fuzz/vulnerability kayıtları bu oturumdaki ayrı kanıtları korur. Native macOS testi yapılmadı; /bin/true kullanılan cross-compile çıktısı runtime PASS sayılmaz.
0.5 doğrulama kanıt dosyaları (ignored local outputs): `.cache/windows-production-tests.jsonl`, `.cache/linux-production-validation.txt`, `.cache/offline-production-demo.jsonl`. Native demo dizini `.cache/demo-4b4467037a3940a8a7a60528f5455e44`; source `hello.txt` aynı kaldı. `scripts/check.ps1` ile aynı format/module/vet/test/build/doctor adımları JSON test kaydıyla uygulandı. Linux pinned validation script `.cache/validate-production-linux.sh`, vet/race ve binary build'i çalıştırdı. Darwin arm64 package/test binary cross-compile `go test -exec /bin/true` ile **COMPILE_ONLY**'dır; native macOS PASS değildir. Encoding/index değişmediği için fuzz tekrarları ve değişmeyen dependency graph'ın önceki govulncheck sonucu tarihsel kanıt olarak ayrı tutuldu.

P02/P03'ün bu dilimi 0.5.0-dev'dir; 260 PASS uygulamanın %100 üretim kabulü değildir. A/B/stable release kapıları CLOSED; gerçek remote/local inference kullanıcı tercihiyle DEFERRED; tam protected runner/observer/check revision, migration/delete/control reserve, live apply/restore, supervisor/TUI, packaging/pilot ve bağımsız eval açık.