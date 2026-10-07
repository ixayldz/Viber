# Geliştirme

Go 1.27.1. Core tek Go module. Toolchain/dependency caches ve binary Git'e girmez.

```sh
go mod download
go test ./...
go vet ./...
go build -trimpath -o bin/viber ./cmd/viber
./bin/viber doctor --json
./bin/viber snapshot --root . --json
```

Windows'ta binary adı bin/viber.exe. Bu checkout'ta checksum doğrulanan portable toolchain .tools/go/bin/go.exe altında; cache .cache içinde. scripts/check.ps1 aynı komutları çalıştırır.

Snapshot/proposal-check geliştirici preview'udur. Canlı source, index ve Git refs değiştirilmez. Capture iki exact scan olsa da BEST_EFFORT; atomic veya sandbox assurance değildir. Symlink/special/case collision/quota unsupported. Secrets (.env/.pem/.key), tooling/cache ve binary scope'u exclude edilir; bu filtre tüm hassas veriyi bulduğu garantisi değildir. Git ignored capture ve junction race conformance backlog'dadır.

Proposal formatı: schema_version=1, id/task_id/spec_version/base_snapshot/policy_epoch/kernel_generation/read_set/changes. FILE read condition blob hash'i, ABSENT boş digest, LISTING directory digest'i taşır. After bytes JSON base64; before_digest exact preimage. policy JSON array her authoritative restriction layer'ı içerir; authority JSON current TaskState'tir. CLI current capture'dan preview oluşturur, eski whole-snapshot proposal'ı konservatif reddeder. Kütüphane ayrı immutable base/current ile bağımsız kullanıcı değişikliklerini koruyabilir; persistent snapshot blobs henüz yoktur.

Canonical v1 sorted JSON object keys, ordered arrays, explicit null, signed 64-bit decimal integer, UTF-8 text. Float/exponent/-0, duplicate fields, unknown typed fields, surrogate escapes, excessive nesting ve trailing documents reddedilir. Null/absent ve array order farklı digest; object field order aynı digest. Metadata domain prefix'i file byte hash'inden ayrıdır. JSON Schema artifact export henüz A1 backlog'dadır.

Store testleri SQLite WAL/FULL, OS owner lock, global/task sequence, event+projection atomic transaction, hash chain/payload integrity, command ID dedup ve checkpoint gösterir. Private Windows ACL, payload deletion, blob durability, backup/restore/migration suite, store quota, secure IPC ve backend fencing tamamlanmadığı için store CLI'de production session olarak açılmaz.

Run/resume exit 3 + UNSUPPORTED_CAPABILITY verir. Host shell fallback yok. Kalite predicate'i yalnız trusted kernel/runner facts üzerinde çalışır; bool alanlarını model payload'ından kabul etmek kesinlikle yasaktır. Critical observer, protected runner ve read-only source conformance henüz yoktur.

Testler state/policy/encoding/context/verification/store/workspace/CLI sınırlarında adversarial örneklerdir. Release family tamamlanması docs/RELEASE_GATES.md ile ayrıca takip edilir. Sonraki paket docs/IMPLEMENTATION_PLAN.md'deki A2/A3/A4'tür.