# Offline retrieval report scoring

`scripts/retrieval-evaluation.py` checks and scores imported Viber `fs_search` replies against a frozen human-labeled DEV/HOLDOUT dataset. It never runs the retrieval code, creates hit rankings, chooses a best run, or upgrades quality. A valid report is **descriptive evidence about the supplied bytes only**; it is not release evidence, adoption approval, or a model-quality claim.

The current journal history API returns event envelopes and event payloads, not the raw model tool-call/reply messages. This runner therefore accepts an explicit import envelope containing the original `fs_search` call arguments and exact `model.Reply.Content` string. The envelope’s task and journal cursors, timestamps, source revision, and executable digest are declarative inputs: this tool does not verify them against an owner store, a signed build, Git history, or an attestation service. The report output labels that limitation and always sets `release_evidence`, `adoption_allowed`, and `quality_claim` to `false`.

## Run it

Pin the exact dataset file bytes before collecting reports. Supply one materialized source root for every repository entry. The roots must contain exactly the frozen file inventory; the runner hashes the files and checks every labeled span and returned hit against those bytes.

```powershell
# Read the digest recorded when the manifest and labels were frozen, before the run.
$datasetHash = (Get-Content C:\eval\retrieval-dataset.sha256 -Raw).Trim()
py -3 -B scripts/retrieval-evaluation.py `
  --dataset C:\eval\retrieval-dataset.json `
  --dataset-sha256 $datasetHash `
  --source-root repo-dev=C:\eval\sources\repo-dev `
  --source-root repo-holdout=C:\eval\sources\repo-holdout `
  --reports C:\eval\fs-search-imports.jsonl `
  --output C:\eval\retrieval-metrics.json
```

The output path must be new. Without `--output`, metrics go to stdout. The dataset hash should come from a freeze/preregistration record kept separately from the dataset and report files. A local hash written after the experiment is only an integrity check; it does not prove that labels or conditions were frozen before the run.

The runner has no network access and does not launch Viber. `--self-test` exercises only temporary, generated synthetic bytes:

```powershell
py -3 -B scripts/retrieval-evaluation.py --self-test
```

That check is not a retrieval benchmark and must not be reported as one.

## Frozen dataset manifest

The JSON object uses schema 1 and rejects unknown keys, duplicate JSON object keys, floats, invalid UTF-8, and inputs over the bounds in the script. Its shape is:

```json
{
  "schema_version": 1,
  "kind": "VIBER_RETRIEVAL_DATASET_V1",
  "dataset_id": "retrieval-2026-01",
  "protocol_digest": "<64 lowercase hex characters>",
  "label_provenance_digest": "<64 lowercase hex characters>",
  "frozen_at_utc": "2026-01-02T00:00:00Z",
  "conditions": [
    {
      "id": "current",
      "build_sha256": "<64 lowercase hex characters>",
      "source_revision": "<full 40- or 64-character source revision>",
      "label": "condition name"
    }
  ],
  "repositories": [
    {
      "id": "repo-dev",
      "split": "DEV",
      "repository_digest": "<inventory digest>",
      "source_snapshot_digest": "<Viber candidate snapshot digest>",
      "policy_digest": "<Viber effective policy digest>",
      "history_cutoff_utc": "2025-12-31T00:00:00Z",
      "snapshot_at_utc": "2026-01-01T00:00:00Z",
      "files": [
        {"path": "src/search.go", "sha256": "<file digest>", "size_bytes": 1200, "authorized": true}
      ]
    }
  ],
  "queries": [
    {
      "id": "query-001",
      "repository_id": "repo-dev",
      "query": "SearchCoordinator",
      "intent": "IDENTIFIER",
      "ks": [1, 5, 10],
      "relevant_spans": [
        {"path": "src/search.go", "file_sha256": "<file digest>", "byte_start": 40, "byte_end": 58, "span_sha256": "<exact span digest>"}
      ],
      "task_ids": {"current": "retrieval-dev-001"},
      "labels_reviewed_at_utc": "2026-01-01T12:00:00Z"
    }
  ]
}
```

The example values are placeholders, not a supplied dataset. A real manifest has at least one repository and labeled query in each of DEV and HOLDOUT, and at least one frozen condition. A query has one preassigned task ID per condition. The same repository ID, repository content digest, source snapshot digest, or normalized query cannot cross DEV/HOLDOUT. Each split has distinct source repositories. Queries and source files are bounded by the runner.

Repository `files` are sorted by normalized `/`-separated relative path and enumerate the full materialized snapshot. Each path is unique, collision-free ignoring case, regular, and digest-bound. `repository_digest` is plain SHA-256 of UTF-8 canonical JSON for the sorted array of `{path, sha256, size_bytes}` objects. The `authorized` flag marks the subset allowed into this query’s ranked `fs_search` scope; the runner derives and checks Viber’s source-set digest from those paths and hashes. Source roots may not add or omit files.

`source_snapshot_digest` and `policy_digest` are the Viber metadata bindings expected in the runtime reply. The runner verifies that the reply uses both exact digests and that the index source set matches the authorized files. It does not reconstruct Viber’s whole candidate snapshot (which also binds capture metadata and Git state), validate the policy’s provenance, or establish that the declared history cutoff is true. Timestamps and human label provenance are unsigned declarations. `history_cutoff_utc <= snapshot_at_utc <= labels_reviewed_at_utc <= frozen_at_utc` is required.

Each `relevant_spans` item is a nonempty, nonoverlapping half-open byte interval `[byte_start, byte_end)` in an authorized source file. Its whole-file digest and exact span digest must match the supplied source root. A returned hit is relevant to a label when their byte intervals overlap in the same file with the same digest. Labels are positive source locations; this protocol does not measure absence proof, answer correctness, precision, or semantic completeness.

Each condition freezes an executable SHA-256, a full source revision, and an operator-readable label. The runner compares those declarations to every report row. This detects inconsistent rows but does not verify that the executable was built from that revision or ran the reported query.

## Imported report JSONL

There is one row per frozen `(condition, query)` assignment. Duplicate rows are rejected; the runner never picks the fastest or best attempt. A missing row stays visible as `MISSING_REPORT` in the assignment denominator. The envelope has these fields:

```json
{
  "schema_version": 1,
  "kind": "VIBER_FS_SEARCH_IMPORT_V1",
  "dataset_sha256": "<exact frozen dataset file SHA-256>",
  "run_id": "run-001",
  "condition_id": "current",
  "query_id": "query-001",
  "task_id": "retrieval-dev-001",
  "task_seq": 12,
  "journal_seq": 44,
  "call_id": "call-001",
  "observed_at_utc": "2026-01-03T00:00:00Z",
  "source_revision": "<same full revision as the frozen condition>",
  "build_sha256": "<same executable digest as the frozen condition>",
  "arguments_json": "{\"query\":\"SearchCoordinator\",\"mode\":\"ranked\",\"intent\":\"IDENTIFIER\",\"candidate_digest\":\"<snapshot digest>\",\"limit\":10}",
  "arguments_sha256": "<SHA-256 of the exact UTF-8 arguments_json string>",
  "fs_search_reply": "{\"retrieval\":{...},\"coverage\":{...},\"scope\":\"CAPTURED_POLICY_SCOPE; RANKED_POOL_IS_NOT_ABSENCE_PROOF\"}",
  "reply_sha256": "<SHA-256 of the exact UTF-8 fs_search_reply string>",
  "wall_micros": 12000,
  "usage": {"known": true, "input_tokens": 500, "output_tokens": 30}
}
```

`arguments_json` is the original raw JSON string from the `fs_search` model tool call. `fs_search_reply` is the exact string in Viber’s corresponding tool reply, not a re-created ranking object or a report from the `rank-policy` evaluator. The two SHA-256 fields cover the decoded UTF-8 strings byte-for-byte. The reply must contain Viber’s current `retrieval` result, first-page `coverage`, and exact scope string. The runner checks the retrieval version, candidate and policy binding, candidate source set, query/intent, byte offsets, returned exact bytes, hit file digests, hit line numbers, page bounds, and partition evidence when present. A supplied `candidate_digest` argument must match; the actual retrieval reply must carry the exact candidate binding either way. An omitted `intent` follows Viber's `IDENTIFIER` default, and an omitted `limit` follows Viber's 32-hit default.

The `usage` object must say either `{"known":true,"input_tokens":N,"output_tokens":N}` with nonnegative integers or `{"known":false,"input_tokens":null,"output_tokens":null}`. Totals include only known provider-reported rows and are labeled as such; missing and unknown usage are never treated as zero. `wall_micros` is an optional wrapper-measured nonnegative integer or `null`. The Viber reply’s `query_micros` separately measures its retrieval query; it does not measure model latency or end-to-end user wait time.

Rows must be timestamped at or after the dataset freeze, share one run ID, use their frozen task assignment, and carry unique call and task cursors per condition. Task/journal cursors are still self-reported in this offline tool; the runner does not open the owner store to corroborate them. Replies that fall back to literal mode are counted as fallback/unknown coverage, not as a ranked empty result. A timeout or absent response is represented by no report row and remains missing; do not fabricate a zero-hit reply.

## Metrics and denominators

The result reports each condition and split independently. `assigned_queries` is the full frozen assignment count. `reports_present`, fallback rows, partial source coverage, missing reports, and unknown reasons remain separately visible. Only initial ranked replies that are not partitioned and whose ranked pool was not internally truncated enter the Recall@K denominator. Recall@K needs the exact first `K` ranks, so a paged result is sufficient when the page includes all `K` hits. `known_coverage_fraction` is the Recall@K-eligible query count divided by all assigned queries. This is a conditional metric, not an imputation for missing or partial queries.

Recall uses exact labeled spans. `micro_recall` is retrieved relevant spans divided by all labeled spans in eligible queries; `macro_query_recall` first computes recall per eligible query, then averages those query values equally. Each K includes the exact query denominator. MRR is the mean reciprocal rank of the first hit overlapping any relevant span; a full ranked pool is required to score it, and eligible queries with no relevant hit contribute zero. Replies whose pool extends beyond the imported first page are counted in `mrr_incomplete_rank_pages` and excluded from the MRR denominator, never treated as zero. Each value includes its denominator. No-relevant-label or zero-denominator values are `null`.

Partitioned retrieval replies are source-checked against Viber’s partition plan but receive `PARTITIONED_SOURCE_NO_GLOBAL_RANK`: Viber returns a rank order local to each partition, so this runner does not invent a cross-partition order. Internally truncated pools are likewise excluded and counted as `RANKED_POOL_TRUNCATED`. Retrieval query p95 uses raw `query_micros` from every validated ranked reply, including partial-coverage replies; it uses the nearest-rank percentile and reports its sample count. Wrapper wall-time p95 uses only non-null wall-time samples. Token counts report known totals, known/unknown report counts, and nearest-rank p95 for input, output, and combined tokens among known-usage rows. None of these p95 values are product-level latency or cost acceptance.

DEV and HOLDOUT use the same formulas, but HOLDOUT output is descriptive only. This script applies no threshold, comparison decision, adoption rule, or release gate. A trustworthy preregistration process must pin the dataset hash before collecting reports and independently retain the assignment, source, build, report, and label evidence. Local timestamps, unsigned JSON, imported reports, and self-reported cursors are not tamper-proof evidence.
