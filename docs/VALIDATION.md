# Doğrulama kaydı

Tarih: 8 Ekim 2026. Ürün: 0.2.0-dev. Bu sonuçlar offline engineering profiline aittir; PRD A–D üretim kabulü değildir.

| Kontrol | Sonuç / kapsam |
|---|---|
| gofmt / mod verify / go vet | PASS; pinned module checksums, Windows native ve Linux |
| Windows test | 168 test/alt test PASS; 2 host symlink yetkisi SKIP; 0 FAIL |
| Linux race tests | PASS; tüm paketler, linux/amd64 |
| Linux strict JSON fuzz | PASS; son turda 30.442 execution, corpus 178 |
| Linux Git index fuzz | PASS; son turda 122.961 execution, corpus 14 |
| Windows binary / offline demo | PASS; fixture→candidate→export→backup→fresh restore→historical replay |
| Linux binary / doctor | PASS; linux/amd64 |
| macOS tüm paket + CLI cross-build | PASS; darwin/arm64; native macOS smoke yapılmadı |
| Gerçek Docker engineering tests | PASS; Windows host/Linux engine; readonly root/source, non-root, network-none, forged PASS+exit7, timeout child cleanup |
| Provider adapters | PASS; offline OpenAI/Anthropic vectors ve loopback Ollama fixtures; gerçek inference yapılmadı |
| Gerçek Git patch interoperability | PASS; disposable temp dizininde CRLF, Unicode, no-final-newline, boş dosya, ekleme/silme |

Windows iki SKIP: workspace ve fileguard symlink oluşturma testleri; host yetkisi yok. Aynı fixture'lar Linux race turunda geçti. Hostile junction/namespace escape ailesi bu sonuçla kapanmaz.

Linux test image'i resmi golang:1.27.1-bookworm@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66. Source/module cache readonly, ağ kapalı, rootfs readonly, cap-drop ALL, no-new-privileges, bounded CPU/memory/PID ve exec scratch kullanıldı. İlk Linux denemesinde tmpfs noexec test binary'lerini engelledi; düzeltmeden sonra tüm aşamalar geçti. Test ortamı tam backend conformance değildir.

Davranış kanıtları:

- Immutable blob publication DB pointer'dan önce; collision overwrite yok. Task/case scope, eksik blob, candidate bytes/mod/manifest kurcalaması reddedilir.
- Native index v2/v3/v4/SHA-256/worktree capture host Git helper/config/filter çalıştırmaz. Dirty/staged/untracked/index korunur. Index/ignore/read-set/listing/absence değişimi proposal'ı invalidate eder.
- Raw intent, whole task document, response, bütçe ve candidate kalıcıdır. Unknown usage/admitted effect restart/restore'da retained; blind retry yok.
- Request exact action/spec/policy/candidate/expiry'ye bağlıdır. Resume onay değildir. One-shot approval ikinci proposal'a yayılmaz. Response command dedup ve payload conflict denetlenir.
- Event/payload/projection/command/sequence transaction; crash/rollback injection, projection/hash chain/tail ve command-result corruption fail-closed olur.
- Snapshot backup CAS+SQLite closure'ını birleştirir; manifest son yayınlanır. Corrupt/missing/unfinished/traversal/duplicate/schema/watermark/journal/reference saldırıları target yayınlamaz.
- Fresh restore mevcut store/source'u değiştirmez; pending reservation/approval bağları korunur. Model/tool effect çalıştırılmaz.
- Historical inspect/replay gerçek task cursor'unu verir ve full retained journal denetler. Event paging task/global sequence'yi ayırır.
- Export exact before/after bytes + patch + report + manifest taşır. Binary text patch coverage açıkça false; source/task quality değişmez.
- Model VERIFIED, stdout PASS, exit0 ve limited-result approval güçlü kalite üretmez. Offline delivery exit2 ve açık required obligation taşır.
- Policy/epoch/generation/barrier intersection ve strict canonical JSON testleri korunur. Kontrol komutları boş/yanlış store'da DB başlatmaz.

İlk 0.1.0-dev foundation turunda govulncheck v1.8.0 known reachable vulnerability bulmadı; dependency değişikliğinde tekrar taranır.

Açık kabul: tam sandbox escape/egress/fencing/clock; real provider+streaming; semantic spec/goal review; protected check closure/observer; complete budget/control reserve; secure IPC/supervisor/steering; live apply/restore concurrency; retention/delete/tombstone/migration/OS power-loss; platform install/signature/SBOM, native macOS smoke ve independent benchmark/pilot. [Release kaydı](RELEASE_GATES.md) kapıları kapalı tutar.
