# İlk geliştirme dilimi doğrulama kaydı

Tarih: 7 Ekim 2026. Ürün sürümü: 0.1.0-dev. Başlangıç Git revision: 3dea1f6. Bu kayıt engineering foundation içindir; PRD A–D üretim kabulü değildir.

| Kontrol | Sonuç / kapsam |
|---|---|
| gofmt | PASS; cmd ve internal Go kaynakları |
| go mod verify | PASS; pinned module checksum doğrulaması |
| go vet | PASS; Windows native |
| Windows test | 76 test/alt test PASS; 1 SKIP; 0 FAIL |
| Windows build + doctor | PASS; windows/amd64 native |
| Windows strict-decoder fuzz | PASS; son 10 saniyelik turda 48.331 execution |
| Linux test/race/fuzz/build | PASS; linux/amd64; go vet, bütün modüllerde race tests, 73.879 fuzz execution, build ve doctor smoke |
| macOS tüm paket + CLI cross-build | PASS; darwin/arm64; native macOS smoke yapılmadı |
| govulncheck v1.8.0 | No vulnerabilities found; bu çalıştırmadaki bilinen/reachable açık taraması |
| CLI snapshot smoke | PASS; repository regular-file preview; BEST_EFFORT |
| Staged diff whitespace | PASS |

Windows skip: TestExcludedAbsenceAndSymlinksCannotBeAssumed; host symlink oluşturma yetkisi yok. Excluded-scope ve symlink assurance release family tamamlandı sayılmaz. Linux'ta aynı symlink fixture geçti; bu path kontrolü gerçek sandbox escape conformance'ı yerine geçmez.

Linux test image'i resmi golang:1.27.1-bookworm, digest sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66. Test invocation ağ kapalı, source/module cache salt okunur, read-only rootfs, ayrı scratch, cap-drop ALL, no-new-privileges ve kaynak limitleriyle çalışır. Bu geliştirme test ortamı Viber execution backend conformance'ı değildir.

Önemli davranış kanıtları:

- Gerçek ayrı test process'i SQLite/lock kapatmadan ölür; committed journal korunur, OS owner lock bırakılır, yeni generation explicit recovery gerektirir.
- SQL trigger event insert sonrasında failure enjekte eder; event/payload/projection/command/sequence transaction birlikte rollback olur.
- Aynı command ID aynı payload ile bir kez append; farklı payload COMMAND_ID_CONFLICT. Concurrent callers tek event üretir.
- Payload/hash chain/tail/projection corruption admission/replay'i durdurur; future store schema downgrade olmaz.
- Global store_seq ile task_seq ayrılır; typed replay ve checkpoint aynı state üretir; replay dış eylem çalıştırmaz.
- User edit stale proposal'ı reddeder; negative read/listing phantom dosya/boş dizinle invalidate olur; bağımsız user edit preview'da korunur; live source yazılmaz.
- Birden fazla input ayrı durable kimlik taşır; tek acknowledgement diğer input'un bariyerini kaldıramaz.
- Policy katmanları kesişir; repo geniş tercih, eski epoch/generation ve remote fallback deny'ı aşamaz.
- Mandatory context sığmazsa CONTEXT_TOO_SMALL; optional bloklar bütün olarak omit edilir.
- Missing raw intent/empty criteria/wrong candidate/environment/check digest, forged result channel, live child, zero tests veya unknown check kind VERIFIED üretemez.
- Normal/critical check trust, rerun failure, waiver, unknown guard/proven failure, candidate quality ve live delivery conflict ayrı hesaplanır.
- Strict JSON duplicate/case-alias/unknown/missing fields, invalid UTF-8, scalar null, float/exponent/-0 ve oversized/nested input reddeder. Exact-byte hash CRLF'yi korur.

Henüz doğrulanmayan release koşulları: gerçek sandbox escape/egress/credential suite; iki remote ve bir local provider; raw input/blob durability; backup/migration/retention/delete/ACL; budget/control reserve; secure IPC/supervisor; protected OS check closure/result channel; full coding-agent recovery demo; apply/restore concurrency; TUI; native macOS package; independent benchmark/pilot. [Release kaydı](RELEASE_GATES.md) bu kapıları CLOSED/NOT_IMPLEMENTED tutar.