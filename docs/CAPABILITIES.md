# Güncel kullanıcı yetenekleri ve kabul sınırları

11 Ekim 2026. Bu tablo aktif kullanım kapsamıdır. Tarihsel kanıtlar [VALIDATION](VALIDATION.md), PRD saldırı family'lerinin tam kapanışı [RELEASE_GATES](RELEASE_GATES.md), kalan implementasyon ve kabul [KEYLESS_DELIVERY_PLAN](KEYLESS_DELIVERY_PLAN.md) içindedir. Test sayısı veya implementasyon varlığı production kabulü değildir.

| Kullanıcı yeteneği | Kullanılabilir kapsam | Açık kabul/eksik |
|---|---|---|
| Kalıcı görev/candidate/diff/export | CLI, fixture loop, immutable capture/candidate, task/store replay, bounded approval | Tam apply/owned restore ve hostile writer matrisi |
| Model bağlantısı | Offline fixture; Ollama adapter; resmî ChatGPT giriş kodu; OpenAI/Anthropic API JSON/SSE | Gerçek hesap/model/provider kabulü ayrı; fixture gerçek inference değildir |
| Context retrieval | Source-bound FTS5/BM25/trigram, exact span/paging/cache/log processing | Semantic AST/LSP/history/reranker ve bağımsız effectiveness/reference p95 |
| Verification | Korunan exact STDIO oracle, candidate binding, fresh merged check, ayrı hidden evaluator owner | Genel framework/discovery/protected closure ve V0–V5 tam matrisi |
| Silme/retention | Explicit task-content purge, family watermark/managed copies/restored owner, açık rızalı EXPIRE; default KEEP; owner timer/CLI/TUI | Metadata/audit retention, UNKNOWN raw redaction, external checkpoint/compaction |
| Retention tarama önceliği | Bounded current-authority/root/policy bağlı scheduling CAS; cold reopen adalet testi; a00a6b3 foundation kabulü | Disk admission hatasında VOLATILE görünür; scheduling cursor silme yetkisi değildir |
| Disk/operation maintenance | Fiziksel 32 MiB control reserve, gerçek bounded ENOSPC, replenish/status; inactive typed operation lease retirement | Ortak managed payload/temp/final/outstanding kota; persistent owner/legacy lifecycle |
| Process backend | Ölçülen rootful profil; CE 28.0.4/rootless/cgroup2/systemd gerçek restart ve hostile enforcement | Farklı platform/backend/profile ölçümü, aggregate fence lifetime physical accounting |
| Background owner/TUI | Detach/attach, durable cursor, pause/cancel, temel queue/context/plan/status/recovery araçları | Provider delta/gap/resync, tam goal scheduling/native PTY/restore yolculuğu |
| Policy/extension/team | Temel privacy restrictions/config | Current admin revision, isolated MCP/extensions/skills/team/remote executor |
| Eval | Ayrı hidden evaluator owner ve bütün attempt/UNKNOWN accounting; paired rapor | Preregistered dataset/build/holdout, gerçek model kalite ve insan pilotu |
| Dağıtım | [Public engineering ön sürümü](https://github.com/ixayldz/Viber/releases/tag/engineering-1f145f6d1062129a3b2506b1a7ab570ad0739416); üç native portable smoke, repo/workflow/ref/SHA bağlı OIDC consumer verification, MIT/dependency lisansları/CycloneDX SBOM; anonim indirme/readback | Otomatik install/update/rollback; OS certificate signing/notarization; bağımsız pilot ve stable A–D gates; publisher düzeltmesinin ayrı native write kabulü |

Yayımlanan source `1f145f6d1062129a3b2506b1a7ab570ad0739416` [exact-SHA CI](https://github.com/ixayldz/Viber/actions/runs/38087257996) bütün **12 native/foundation/package/provenance job'ında SUCCESS**. Windows permission fixture, üç OS full/vet/vulnerability/migration, Linux full race/tüm fuzz, gerçek rootful/rootless ve iki native vault kabulüne üç portable paket ve consumer provenance doğrulaması eklendi. Tag publisher'ın ilk draft lookup failure'ı tarihsel kayıtta korunur; original draft explicit ID ile yeniden doğrulanıp yayınlandı. Yeni revision'ın kabulü önceki başarılı revision'dan miras alınmaz. Bu engineering dağıtımı PRD'nin stable kapılarını açmaz.
