# ADR 0002 — Offline engineering runtime ve teslim sınırı

Durum: 0.2.0-dev mühendislik profili için kabul. A/B/stable ürün kabulü değildir.

Foreground owner Go kernel + WAL/FULL SQLite journal + task-scoped immutable CAS kullanır. Ham prompt/fixture/response bytes CAS'ta, document pointer ve typed projection journal transaction'ında kalır. Candidate source'tan ayrıdır. Session mutation mutex'i process içi public mutating API'leri serileştirir; OS owner lock ikinci süreci reddeder. Peer-auth IPC henüz yoktur.

Model provider adapter'ları inference gerçekleştirmeyen fixture testleriyle geliştirildi. CLI yalnız fixture loop açar. OpenAI/Anthropic/Ollama canonical continuation, call/result boundaries, authoritative integer JSON, transport limits ve current authority kontrolü aynı model paketindedir. Nonstream profile A4'ün streaming/real endpoint şartını karşılamaz.

Native read/list/search/propose, durable operation intent ve token upper-bound reserve üzerinden yürür. Unknown usage/admitted effect reconnect veya backup restore sırasında retained kalır. Review approval exact args/spec/policy/candidate/expiry bağı taşır; response receipt command ID ile dedup edilir. Onay gelecekteki işlemlere veya yeni candidate'a aktarılamaz.

Docker backend yalnız explicit pinned-image offline readonly computation profilidir. Source/check trust observer tamamlanmadığından Docker sonucu kaliteyi VERIFIED yapamaz. CLI operatör policy/authority dosyaları trusted input'tur, model tarafından üretilecek yetki belgesi değildir.

Export exact changed bytes + patch + report + son manifest üretir. Text patch CRLF/no-newline/Unicode/empty/delete fixture'larında gerçek Git ile doğrulanır. Binary changeset text coverage'ı açıkça eksik işaretlenir. Live apply/restore concurrency desteği yoktur.

Backup SQLite VACUUM INTO snapshot + tüm published immutable artifacts içerir. Restore hashes/schema/paths, full journal replay ve historical document closure doğrulamadan yeni target yayınlamaz. Current profile hiç deletion/GC yapmaz; watermark 0 zorunludur. Bu karar gelecekteki deletion watermark/tombstone desteğinin yerine geçmez.

Kalite terminal durumdan ayrıdır. Offline demo ancak explicit limited-result policy veya bağlı kullanıcı onayıyla FINISHED/UNVERIFIED/SATISFIED (exit 2) olabilir; required verification obligation açık kalır. Strict exit 0 kapalıdır. Model metni, test stdout'u, Git apply interoperability testi veya bir request approval'ı goal coverage kanıtı değildir.
