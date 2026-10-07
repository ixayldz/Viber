# Release kanıt kaydı

PRD §28 family kimlikleri sabit. Fixture yalnız çalışıp beklenen davranışı gözleyince PASS. NOT_IMPLEMENTED skipped-yeşil sonuç değildir; unit/spike bütün family'yi kapatmaz.

| Family | Guard | İlk dilim | Tam release |
|---|---|---|---|
| 1 | Partial write crash | Preview writes yok | NOT_IMPLEMENTED |
| 2 | Lost response/unknown effect | Conservative unknown verdict | PARTIAL |
| 3–5 | Candidate/env/stale edit | Wrong binding + phantom preview tests | PARTIAL |
| 6 | Steering | Pure reducer/admission | PARTIAL |
| 7–11 | Lease/budget/process/FS isolation | Static denial/path tests | NOT_IMPLEMENTED (OS) |
| 12–13 | Forged PASS/check weakening | Receipt guards | PARTIAL |
| 14–15 | Index/partial model stream | No adapter/index | NOT_IMPLEMENTED |
| 16 | Context overflow | Mandatory preflight tests | PARTIAL |
| 17 | Compaction policy | No compactor | NOT_IMPLEMENTED |
| 18 | Privacy fallback | Restriction intersection | PARTIAL |
| 19 | Plugin isolation | No plugin | NOT_IMPLEMENTED |
| 20–21 | Corruption/two owners | Journal/OS lock tests | PARTIAL |
| 22–24 | Restore/reconnect/telemetry | Execution/export absent | NOT_IMPLEMENTED |
| 25–28 | Preview guards/origin/integrity/delivery | Pure predicate; real runner absent | PARTIAL |
| 29 | Clock/lease | No backend lease | NOT_IMPLEMENTED |
| 30–32 | Owner/children/pause/dedup | Lock, reducer, predicate, dedup | PARTIAL |
| 33–34 | Reserve/delete/backup | No ledger/retention | NOT_IMPLEMENTED |
| 35–36 | Sequence/replay/intent | Journal/checkpoint/empty intent tests | PARTIAL |
| 37 | Independent evaluator | No benchmark | NOT_IMPLEMENTED |
| 38 | Host Git/OSC/secret output | Terminal escape; no Git execution | PARTIAL |

A gate CLOSED. B preview CLOSED. Stable A–D CLOSED. Test artifacts CI ve go test ile üretilir. Gerçek sandbox/provider/backup/migration/platform/privacy checks kapılar açılmadan zorunludur.