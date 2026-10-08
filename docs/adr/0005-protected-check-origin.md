# ADR 0005 — Mutation öncesi protected check origin

Tarih: 8 Ekim 2026. Durum: 0.5.0-dev engineering diliminde uygulanmış; strong verification kabulü açık. PRD §21 ve normatif ADR 009 kararının origin/closure kısmını uygular.

Problem: candidate ilk verification'dan önce test/helper/config/discovery girdilerini değiştirirse sonradan dondurulan test listesi zayıflatılmış olabilir. Sadece komut, stdout, exit 0 veya ön/son hash doğru bir acceptance oracle'ı değildir.

Karar: trusted CLI operator `--check-plan FILE` ile sürümlü, bounded CheckDefinition verir. Model/tool/repo preferences bu planı oluşturma veya değiştirme yetkisi taşımaz. Definition, check/requirement IDs, argv, runner digest, selection, exact expected discovery ve file/recursive tree/absent closure kapsamını içerir. Baseline exact bytes, modes, directories ve exclusions üzerinden scope digest'leri çıkarılır. Planın bağımsız kopyası alınır ve sırasız ID/scope kümeleri normalize edilir; argv sırası korunur.

Origin, ilk spec/raw-input/baseline/logical source root'a bağlanır. Origin'in tamamı immutable task document CAS'ına girer; task journal pointer'ı yayınlanmadan candidate write admit edilmez. `TaskSpec.protected_check_origin` domain-separated origin digest'idir. Load, historical backup ve restore origin'i immutable baseline'dan yeniden türetir; yeni origin'in kaldırılması legacy marker'a sessiz dönüşemez.

Proposal ve review preflight, protected bytes/modes/absence/tree üyeliği değişince POLICY_DENIED verir. Kapsamdaki test ekleme/silme, empty directory, config/helper değişimi ve absent config/tree yaratımı da fark edilir. Excluded veya binary baseline nedeniyle tam görülemeyen scope UNSUPPORTED_CAPABILITY'dir; case alias, traversal ve file-parent crossing reddedilir. Production source, protected kapsam dışında normal candidate değişikliğidir.

FrozenChecks, current spec/candidate/check/environment/policy bağını kaydeden bir input contract'tır; PASS receipt değildir. Yeni required criterion eskiden kapsamlı sayılmaz. Quality ve fulfillment ayrı kalır; offline fixture hiçbir şekilde VERIFIED üretmez.

Provenance yalnız `OPERATOR_DECLARED_REPOSITORY_BASELINE`, closure coverage yalnız `EXPLICIT_UNREVIEWED` veya checks yoksa `UNRESOLVED` olur. Kullanıcının plan vermesi dependency closure'ın eksiksizliğini, runner binary'nin gerçekten kullanıldığını, bağımsız oracle'ı veya goal coverage'ı ispatlamaz. CLI/IPC/export özetlerinde strong verification availability false'tur; raw argv/discovery config status'a dökülmez. Compiler'da protected scopes mandatory authoritative metadata'dır; overflow sessiz truncate olmaz.

Mevcut scope revision origin'i korur ve requirement ekleyebilir. Protected beklentiyi değiştiren yetkili check revision protokolü henüz yoktur; bu değişiklik güvenli biçimde reddedilir. Scope'u tüm repo'ya genişletmek production source'u da korur; bu bilinçli conservative scope seçimi olmalıdır. Otomatik dependency discovery, closure review, original/revised check lineage, trusted runner result/discovery kanalı, bağımsız observer, process fencing ve full interval OS enforcement sonraki B5/A3 kabul işleridir.

Eski 0.4 task documents yalnız eski baseline/profile digest marker'ı tam eşleşirse açılır; geçmiş UNVERIFIED sonuçlar yükseltilmez. Yeni typed origin optional document alanıdır; store/schema/reducer v1 korunur. Üst sürüm document'ini anlamayan eski binary fail-closed olur.

Kanıtlar: F13/F26 origin/check/discovery weakening tests; full agent guided/review/auto denial; production change/restart/backup/fresh restore; additive revision coverage denial; strict CLI preflight ve status provenance; real Docker mount/image/argv/scratch/namespace binding kontrolleri. Bunlar bütün §28 ailelerinin release kabulü değildir.