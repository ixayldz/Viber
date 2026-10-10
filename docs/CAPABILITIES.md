# Güncel kullanıcı yetenekleri ve kabul sınırları

10 Ekim 2026. Bu tablo aktif kullanım kapsamıdır. Tarihsel kanıtlar [VALIDATION](VALIDATION.md), PRD saldırı family'lerinin tam kapanışı [RELEASE_GATES](RELEASE_GATES.md), kalan implementasyon ve kabul [KEYLESS_DELIVERY_PLAN](KEYLESS_DELIVERY_PLAN.md) içindedir. Test sayısı veya implementasyon varlığı production kabulü değildir.

| Kullanıcı yeteneği | Kullanılabilir kapsam | Açık kabul/eksik |
|---|---|---|
| Kalıcı görev/candidate/diff/export | CLI, fixture loop, immutable capture/candidate, task/store replay, bounded approval | Tam apply/owned restore ve hostile writer matrisi |
| Model bağlantısı | Offline fixture; Ollama adapter; resmî ChatGPT giriş kodu; OpenAI/Anthropic API JSON/SSE | Gerçek hesap/model/provider kabulü ayrı; fixture gerçek inference değildir |
| Context retrieval | Source-bound FTS5/BM25/trigram, exact span/paging/cache/log processing | Semantic AST/LSP/history/reranker ve bağımsız effectiveness/reference p95 |
| Verification | Korunan exact STDIO oracle, candidate binding, fresh merged check, ayrı hidden evaluator owner | Genel framework/discovery/protected closure ve V0–V5 tam matrisi |
| Silme/retention | Explicit task-content purge, family watermark/managed copies/restored owner, açık rızalı EXPIRE; default KEEP; owner timer/CLI/TUI | Metadata/audit retention, UNKNOWN raw redaction, external checkpoint/compaction |
| Retention tarama önceliği | Bounded current-authority/root/policy bağlı scheduling CAS; cold reopen adalet testi | Güncel exact-SHA CI kabulü ayrıca doğrulanır; disk admission hatasında VOLATILE görünür |
| Disk/operation maintenance | Fiziksel 32 MiB control reserve, gerçek bounded ENOSPC, replenish/status; inactive typed operation lease retirement | Ortak managed payload/temp/final/outstanding kota; persistent owner/legacy lifecycle |
| Process backend | Ölçülen rootful profil; CE 28.0.4/rootless/cgroup2/systemd gerçek restart ve hostile enforcement | Farklı platform/backend/profile ölçümü, aggregate fence lifetime physical accounting |
| Background owner/TUI | Detach/attach, durable cursor, pause/cancel, temel queue/context/plan/status/recovery araçları | Provider delta/gap/resync, tam goal scheduling/native PTY/restore yolculuğu |
| Policy/extension/team | Temel privacy restrictions/config | Current admin revision, isolated MCP/extensions/skills/team/remote executor |
| Eval | Ayrı hidden evaluator owner ve bütün attempt/UNKNOWN accounting; paired rapor | Preregistered dataset/build/holdout, gerçek model kalite ve insan pilotu |
| Dağıtım | MIT, reproducible exact-commit engineering bundle, dependency lisansları/CycloneDX SBOM | Public doğrulanabilir platform paketleri; native install/update/rollback; signing/pilot/stable A–D gates |

`0af0d3f` CI kırmızıdır: Windows permission fixture başarısız; rootful/vault acceptance atlanmıştır. `c63441f` bütün sekiz foundation job'ında başarılıdır. Yeni revision'ın kabulü önceki başarılı revision'dan miras alınmaz. Güncel çalışma ağacındaki düzeltmeler commit/CI kapanmadan doğrulanmış HEAD sayılmaz.
