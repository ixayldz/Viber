# ADR 0016: Reserve öncesi durable privacy authority allocation

10 Ekim 2026. Uygulandı; exact revision native CI sonucu [VALIDATION](../VALIDATION.md) kaydına eklenir. Genel retention/compaction/managed payload quota kabulü değildir.

## Problem

Önceki `ensurePrivacy`, fresh sibling directory'de 32 MiB fiziksel rezerv ve genesis yayınlıyor, ardından owner journal'ına current authority pin'i yazıyordu. Reserve/genesis sonrasında bind başarısızlığı veya process death, sonraki denemede yeni reserve yaratabiliyordu. Dizin prefix'i veya directory age, unbound bir root'u temizleme yetkisi sağlamaz.

## Karar ve sıra

1. Fresh private directory yaratılır ve parent directory sync edilir. Açık root'tan physical identity alınır. Bu aşamada reserve/genesis/task raw içeriği yoktur.
2. Authority ID, canonical directory ve physical identity, journal `payloads` CAS + `privacy_allocation_v1:<digest>` marker'ına tek SQLite transaction ile kaydedilir. Current authority henüz yoktur. Farklı pending allocation reddedilir.
3. Exact pending root yeniden açılır; inode/directory identity ve private permissions denetlenir. Bounded inventory yalnız empty `records`, regular single-link `control.reserve`, exact genesis ve canonical genesis'in prefix'i olan bounded interrupted `.publish-*` dosyalarını kabul eder. Bütün inventory incelenmeden temp kaldırılmaz. Foreign file/genesis, missing/replaced root veya nonempty journal fail-closed durur.
4. Physical reserve aynı root'ta tamamlanır; genesis manifest-last/no-replace yayınlanır.
5. `BindPrivacyAuthority`, current authority marker'ını yayınlama ve pending marker'ını kaldırmayı tek SQLite transaction'da yapar. Yeni pin aynı CAS body'dir. Farklı body bind edilemez. Post-commit caller error/crash sonraki `Open`'da mevcut authority'yi attach eder; ikinci reserve oluşturmaz.
6. Normal family lease/scope attachment bundan sonra yapılır. Pending allocation task content/deletion/restore authority sağlamaz.

Owner SQL generation replay state'i değiştirmez. Allocation current logical task journal/snapshot/ledger'i değiştirmez; sınırlı kontrol metadata'sıdır. Mevcut bound schema-1 authority ve restore/evaluator bindings korunur. Bu yeni marker için SQLite store schema yükseltilmedi; mevcut bounded metadata CAS kataloğu kullanılır.

## Recovery ve kullanıcı davranışı

Normal CLI/owner açılışı pending allocation'ı aynı physical root'ta tamamlar. Missing/replaced/foreign root'ta sessiz yeni allocation yapılmaz. Kullanıcı veya operator bilinmeyen içeriği kaldırmak zorunda bırakılmadan işlem güvenli olarak durur; otomatik recursive delete yoktur. Bu ADR operator self-service repair veya geçmiş tüm orphan'ların migration'ını sunmaz.

Fresh mkdir ile pending CAS publication arasındaki crash küçük, henüz reserve/genesis içermeyen bir directory bırakabilir. Identity kaydı olmadan buna ownership verilmez. Önceki revision'lardan kalan 32 MiB unbound directory'ler yeni pending kayıt olmadan sahiplenilmez veya silinmez. Bu iki tarihsel inventory/cleanup işi K06/K07'de ayrı kalır; yeni reserve-before-bind fault yolu için restart reuse uygulanmıştır.

## Kabul

- Gerçek child test binary allocation/reserve/genesis/bind sonrasında cleanup olmadan exit73 ile sonlanır; production opening path'in internal fault seam'i kullanılır, global environment production davranışını değiştirmez.
- Allocation boundary'de reserve dosyası henüz yoktur. Reserve/genesis crash'larında pending body fiziksel root'u korur; bound crash'ında pending marker yoktur.
- Dört boundary'de recovery ve on `OpenExisting` aynı authority/directory/reserve'i tutar, task snapshot/global resource ledger değişmez.
- Foreign file/root/genesis/temp ve missing root reddi; iki tekrar aynı pending marker'ı ve foreign içeriği korur. Owned partial-genesis temp fixture'ı exact root ve prefix binding ile temizlenir.
- Store testleri pending CAS idempotency/reopen, replacement/corruption deny, exact atomic promotion ve task-document reference ayrımını doğrular.

Process death ve SQLite transaction recovery, fiziksel cihaz power-loss kabulü değildir. `Open` native host permissions/identity ve SQLite durability sözleşmelerine bağlıdır. Kullanıcının sonraki açık onayıyla geçici privileged engine ölçüldü: rootful restart/fencing PASS; rootless kaynak controller'ları olmadığı için UNSUPPORTED. Desteklenen rootless kabulü ayrıca gerekir.
