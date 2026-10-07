# ADR 0001 — Tek Go core ve başlangıç sınırı

Durum: Go seçimi kabul; backend/provider assurance henüz doğrulanmadı.

PRD §25 Rust veya Go tek core ister. Go 1.27.1 seçildi. Official checksum doğrulanan portable toolchain yalnız proje içindedir; PATH/global kurulum değişmez. Pinned pure-Go SQLite driver native compiler ihtiyacını azaltır. Tek binary cross-build edilir; OS lock/ACL/runner platforma özel. Go seçimi OS izolasyonu sağlamış sayılmaz.

PRD ADR 001–013 korunur: kendi loop; tek owner+SQLite+blobs; candidate/journal delivery; observation/claim/decision ayrımı; saf checkpoint; tek model/writer; versioned independent eval; Windows client/Linux execution; protected origin/read-only source; quality/fulfillment/outcome ayrımı; global/task sequence+generation; silinebilir payload+tombstone; independent benchmark.

İlk dilim engineering spike. Host process/remote inference/live apply/restore kapalı. Snapshot/proposal preview explicit developer command, hostile filesystem isolation sağlamaz. 0.3.0-dev offline run/resume, local owner IPC/steering ve snapshot backup/fresh restore uygulanmıştır; protected verification ve store deletion/lifecycle conformance kapıları kapalıdır. Ayrıntılar ADR 0002/0003 ve release kaydındadır.

A3 hedef Linux namespaces/seccomp/cgroups, non-root/rootless; image/version floor gerçek conformance ile seçilir. Docker socket candidate'a verilmez. Read-only source/check, private scratch ve child quiescence olmadan güçlü receipt yok. Native host aynı assurance sayılmaz. Exclusive live access henüz yok; candidate-only default.

A4 remote OpenAI Responses ve Anthropic Messages; local aday Ollama. Model ID/limit/pricing runtime versioned profile. Endpoint conformance olmadan supported sayılmaz; server tools kapalı.

Yeniden açma: build/SQLite/platform acceptance başarısızlığı ölçüm ve ADR revizyonu; ikinci core yok. Toolchain upgrade CI+checksum; active task'ta schema/runtime değişmez.

Kaynaklar: [Go resmi sürümleri](https://go.dev/dl/), [SQLite WAL](https://sqlite.org/wal.html), [modernc SQLite](https://pkg.go.dev/modernc.org/sqlite). Bunlar uygulama assurance kanıtı değildir.