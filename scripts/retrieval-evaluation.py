#!/usr/bin/env python3
"""Score imported, source-bound Viber fs_search reports against frozen labels.

This tool is deliberately offline. It does not run the retrieval engine, infer
whether a report came from Viber, or turn ranking metrics into a quality claim.
"""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import json
import math
import os
import re
import stat
import sys
from pathlib import Path, PurePosixPath
from typing import Any


MAX_DATASET_BYTES = 16 << 20
MAX_REPORT_BYTES = 256 << 20
MAX_REPORT_LINE_BYTES = 2 << 20
MAX_REPLY_BYTES = 1 << 20
MAX_QUERY_COUNT = 10_000
MAX_CONDITIONS = 8
MAX_FILES_PER_REPOSITORY = 100_000
MAX_REPOSITORY_BYTES = 2 << 30
MAX_FILE_BYTES = 256 << 20
MAX_AUTHORIZED_FILES = 10_000
MAX_SPANS_PER_QUERY = 256
MAX_PAGE_SIZE = 128
RETRIEVAL_VERSION = "SOURCE_FTS5_BM25_TRIGRAM_RRF_PARTITION_V3"
PARTITION_VERSION = "AUTHORIZED_SOURCE_PARTITIONS_V2"
TRUST = "UNTRUSTED_SOURCE_BOUND_RANKING_HINT"
SCOPE = "CAPTURED_POLICY_SCOPE; RANKED_POOL_IS_NOT_ABSENCE_PROOF"
PARTITION_SCOPE = "RANKED_POOL_OF_ONE_AUTHORIZED_PARTITION; NO_REPOSITORY_ABSENCE_PROOF"
HEX64 = re.compile(r"^[0-9a-f]{64}$")
SOURCE_REVISION = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
CALL_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$")
UTC_SECONDS = re.compile(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")


class InvalidInput(Exception):
    """A bounded, user-correctable input validation error."""


def need(test: bool, message: str) -> None:
    if not test:
        raise InvalidInput(message)


def object_no_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise InvalidInput("JSON contains a duplicate object key")
        result[key] = value
    return result


def reject_float(_: str) -> None:
    raise InvalidInput("JSON floating-point values are not accepted in manifests")


def reject_constant(_: str) -> None:
    raise InvalidInput("non-finite JSON number is not accepted")


def parse_integer(value: str) -> int:
    if value == "-0" or len(value) > 20:
        raise InvalidInput("JSON integer is outside the signed 64-bit format")
    parsed = int(value, 10)
    if parsed < -(1 << 63) or parsed > (1 << 63) - 1:
        raise InvalidInput("JSON integer is outside the signed 64-bit format")
    return parsed


def parse_json(raw: bytes, maximum: int, label: str) -> Any:
    need(len(raw) <= maximum, f"{label} exceeds its size bound")
    try:
        text = raw.decode("utf-8", errors="strict")
        value = json.loads(
            text,
            object_pairs_hook=object_no_duplicates,
            parse_int=parse_integer,
            parse_float=reject_float,
            parse_constant=reject_constant,
        )
        reject_surrogates(value)
        return value
    except InvalidInput:
        raise
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise InvalidInput(f"{label} is not valid UTF-8 JSON") from exc


def reject_surrogates(value: Any) -> None:
    if isinstance(value, str):
        need(not any(0xD800 <= ord(char) <= 0xDFFF for char in value), "JSON contains an unsupported surrogate escape")
    elif isinstance(value, list):
        for item in value:
            reject_surrogates(item)
    elif isinstance(value, dict):
        for key, item in value.items():
            reject_surrogates(key)
            reject_surrogates(item)


def exact_fields(value: Any, required: set[str], optional: set[str], label: str) -> dict[str, Any]:
    need(isinstance(value, dict), f"{label} must be an object")
    keys = set(value)
    need(required <= keys and keys <= required | optional, f"{label} has missing or unknown fields")
    return value


def is_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def digest_bytes(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def valid_digest(value: Any) -> bool:
    return isinstance(value, str) and HEX64.fullmatch(value) is not None


def canonical(value: Any) -> bytes:
    """Viber-compatible canonical JSON for metadata digests."""
    def visit(item: Any) -> str:
        if item is None:
            return "null"
        if item is True:
            return "true"
        if item is False:
            return "false"
        if is_int(item):
            return str(item)
        if isinstance(item, str):
            encoded = json.dumps(item, ensure_ascii=False, separators=(",", ":"))
            # Go encoding/json (used by CanonicalV1) escapes HTML delimiters and
            # the two JavaScript line separators even with compact output.
            return (
                encoded.replace("&", r"\u0026")
                .replace("<", r"\u003c")
                .replace(">", r"\u003e")
                .replace("\u2028", r"\u2028")
                .replace("\u2029", r"\u2029")
            )
        if isinstance(item, list):
            return "[" + ",".join(visit(part) for part in item) + "]"
        if isinstance(item, dict):
            need(all(isinstance(key, str) for key in item), "metadata object key is not text")
            return "{" + ",".join(visit(key) + ":" + visit(item[key]) for key in sorted(item)) + "}"
        raise InvalidInput("unsupported value in canonical metadata")

    return visit(value).encode("utf-8")


def viber_digest(value: Any) -> str:
    return digest_bytes(b"viber:metadata:v1\x00" + canonical(value))


def utc_time(value: Any, label: str) -> dt.datetime:
    need(isinstance(value, str) and UTC_SECONDS.fullmatch(value) is not None, f"{label} must be UTC RFC3339 seconds ending in Z")
    try:
        parsed = dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc)
    except ValueError as exc:
        raise InvalidInput(f"{label} is not a valid UTC timestamp") from exc
    return parsed


def safe_relative_path(value: Any, label: str) -> str:
    need(isinstance(value, str) and 0 < len(value.encode("utf-8")) <= 4096, f"{label} is not a bounded path")
    need("\x00" not in value and "\\" not in value and ":" not in value, f"{label} is not a portable relative path")
    path = PurePosixPath(value)
    need(not path.is_absolute() and all(part not in ("", ".", "..") for part in value.split("/")), f"{label} escapes the source root")
    need(path.as_posix() == value, f"{label} is not normalized")
    return value


def safe_id(value: Any, label: str) -> str:
    need(isinstance(value, str) and IDENTIFIER.fullmatch(value) is not None, f"{label} is not a valid identifier")
    return value


def safe_call_id(value: Any) -> str:
    need(isinstance(value, str) and CALL_IDENTIFIER.fullmatch(value) is not None, "fs_search call id is invalid")
    return value


def file_inventory_digest(files: list[dict[str, Any]]) -> str:
    inventory = [
        {"path": item["path"], "sha256": item["sha256"], "size_bytes": item["size_bytes"]}
        for item in files
    ]
    return digest_bytes(canonical(inventory))


def source_set_digest(files: list[dict[str, Any]]) -> str:
    refs = [{"Path": item["path"], "Digest": item["sha256"]} for item in files if item["authorized"]]
    return viber_digest(refs)


def scan_source_root(root_value: str, repository: dict[str, Any]) -> dict[str, Path]:
    try:
        root = Path(root_value).resolve(strict=True)
    except OSError as exc:
        raise InvalidInput("a declared source root is unavailable") from exc
    need(root.is_dir(), "a declared source root must be a directory")

    expected = {item["path"]: item for item in repository["files"]}
    observed: set[str] = set()
    paths: dict[str, Path] = {}
    total = 0
    try:
        for directory, dirnames, filenames in os.walk(root, topdown=True, followlinks=False):
            base = Path(directory)
            for name in list(dirnames):
                child = base / name
                if child.is_symlink():
                    raise InvalidInput("symlink directories are not accepted in source roots")
            for name in filenames:
                child = base / name
                relative = child.relative_to(root).as_posix()
                safe_relative_path(relative, "source-root path")
                info = child.lstat()
                need(stat.S_ISREG(info.st_mode), "source root contains a non-regular file")
                need(relative in expected, "source root has a file absent from the frozen inventory")
                need(relative not in observed, "source root contains a duplicate path")
                need(info.st_size <= MAX_FILE_BYTES, "source-root file exceeds its byte bound")
                hasher = hashlib.sha256()
                count = 0
                with child.open("rb") as stream:
                    while True:
                        block = stream.read(1 << 20)
                        if not block:
                            break
                        count += len(block)
                        need(count <= MAX_FILE_BYTES, "source-root file grew past its byte bound")
                        hasher.update(block)
                after = child.stat(follow_symlinks=False)
                need(
                    (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns)
                    == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns),
                    "source file changed while being read",
                )
                reference = expected[relative]
                need(count == reference["size_bytes"] and hasher.hexdigest() == reference["sha256"], "source file differs from its frozen digest")
                total += count
                need(total <= MAX_REPOSITORY_BYTES, "source root exceeds its aggregate byte bound")
                observed.add(relative)
                paths[relative] = child
    except OSError as exc:
        raise InvalidInput("source root could not be read safely") from exc
    need(observed == set(expected), "source root is missing a frozen file")
    return paths


def read_exact(path: Path, start: int, end: int) -> bytes:
    try:
        with path.open("rb") as stream:
            stream.seek(start)
            result = stream.read(end - start)
    except OSError as exc:
        raise InvalidInput("source span could not be read") from exc
    need(len(result) == end - start, "source span is shorter than its declared range")
    return result


def validate_dataset(raw: bytes, expected_digest: str, roots: dict[str, str]) -> dict[str, Any]:
    need(valid_digest(expected_digest), "expected dataset SHA-256 is required")
    actual_digest = digest_bytes(raw)
    need(actual_digest == expected_digest, "dataset bytes do not match the externally pinned SHA-256")
    dataset = parse_json(raw, MAX_DATASET_BYTES, "dataset manifest")
    exact_fields(
        dataset,
        {"schema_version", "kind", "dataset_id", "protocol_digest", "label_provenance_digest", "frozen_at_utc", "conditions", "repositories", "queries"},
        set(),
        "dataset manifest",
    )
    need(is_int(dataset["schema_version"]) and dataset["schema_version"] == 1 and dataset["kind"] == "VIBER_RETRIEVAL_DATASET_V1", "unsupported retrieval dataset schema")
    safe_id(dataset["dataset_id"], "dataset id")
    need(valid_digest(dataset["protocol_digest"]) and valid_digest(dataset["label_provenance_digest"]), "dataset protocol and label provenance digests are required")
    frozen_at = utc_time(dataset["frozen_at_utc"], "dataset freeze time")

    conditions = dataset["conditions"]
    need(isinstance(conditions, list) and 1 <= len(conditions) <= MAX_CONDITIONS, "dataset must freeze one to eight conditions")
    condition_by_id: dict[str, dict[str, Any]] = {}
    for condition in conditions:
        exact_fields(condition, {"id", "build_sha256", "source_revision", "label"}, set(), "condition")
        ident = safe_id(condition["id"], "condition id")
        need(ident not in condition_by_id, "duplicate condition id")
        need(valid_digest(condition["build_sha256"]), "condition build digest is invalid")
        need(isinstance(condition["source_revision"], str) and SOURCE_REVISION.fullmatch(condition["source_revision"]) is not None, "condition source revision must be a full SHA-1 or SHA-256 hex id")
        need(isinstance(condition["label"], str) and 1 <= len(condition["label"]) <= 120 and "\x00" not in condition["label"], "condition label is invalid")
        condition_by_id[ident] = condition

    repositories = dataset["repositories"]
    need(isinstance(repositories, list) and 2 <= len(repositories) <= 1024, "DEV and HOLDOUT repository manifests are required")
    repo_by_id: dict[str, dict[str, Any]] = {}
    root_paths: dict[str, dict[str, Path]] = {}
    split_by_repo: dict[str, str] = {}
    split_repo_ids: dict[str, set[str]] = {"DEV": set(), "HOLDOUT": set()}
    split_repo_digests: dict[str, set[str]] = {"DEV": set(), "HOLDOUT": set()}
    split_snapshot_digests: dict[str, set[str]] = {"DEV": set(), "HOLDOUT": set()}
    need(set(roots) == {item.get("id") for item in repositories if isinstance(item, dict)}, "provide exactly one --source-root REPOSITORY_ID=PATH for each repository")
    for repository in repositories:
        exact_fields(
            repository,
            {"id", "split", "repository_digest", "source_snapshot_digest", "policy_digest", "history_cutoff_utc", "snapshot_at_utc", "files"},
            set(),
            "repository manifest",
        )
        ident = safe_id(repository["id"], "repository id")
        need(ident not in repo_by_id, "duplicate repository id")
        split = repository["split"]
        need(split in ("DEV", "HOLDOUT"), "repository split must be DEV or HOLDOUT")
        need(valid_digest(repository["repository_digest"]) and valid_digest(repository["source_snapshot_digest"]) and valid_digest(repository["policy_digest"]), "repository/source/policy digest is invalid")
        cutoff = utc_time(repository["history_cutoff_utc"], "repository history cutoff")
        snapshot_at = utc_time(repository["snapshot_at_utc"], "repository snapshot time")
        need(cutoff <= snapshot_at <= frozen_at, "repository snapshot must follow its history cutoff and precede dataset freeze")
        files = repository["files"]
        need(isinstance(files, list) and 1 <= len(files) <= MAX_FILES_PER_REPOSITORY, "repository file inventory is outside its bound")
        previous = ""
        folded: set[str] = set()
        total_bytes = 0
        for item in files:
            exact_fields(item, {"path", "sha256", "size_bytes", "authorized"}, set(), "repository file entry")
            path = safe_relative_path(item["path"], "repository file path")
            need(path > previous and path.casefold() not in folded, "repository file inventory must be sorted and collision-free")
            previous = path
            folded.add(path.casefold())
            need(valid_digest(item["sha256"]), "repository file SHA-256 is invalid")
            need(is_int(item["size_bytes"]) and 0 <= item["size_bytes"] <= MAX_FILE_BYTES, "repository file size is invalid")
            need(isinstance(item["authorized"], bool), "authorized file flag must be boolean")
            total_bytes += item["size_bytes"]
            need(total_bytes <= MAX_REPOSITORY_BYTES, "repository inventory exceeds its aggregate byte bound")
        need(file_inventory_digest(files) == repository["repository_digest"], "repository digest does not match its frozen file inventory")
        authorized_count = sum(1 for item in files if item["authorized"])
        need(1 <= authorized_count <= MAX_AUTHORIZED_FILES, "authorized search inventory is outside the retrieval bound")
        source_bytes = sum(item["size_bytes"] for item in files if item["authorized"])
        need(source_bytes <= 32 << 20, "authorized retrieval sources exceed the Viber single-index bound")
        repository["_authorized_source_set"] = source_set_digest(files)
        paths = scan_source_root(roots[ident], repository)
        repo_by_id[ident] = repository
        root_paths[ident] = paths
        split_by_repo[ident] = split
        split_repo_ids[split].add(ident)
        split_repo_digests[split].add(repository["repository_digest"])
        split_snapshot_digests[split].add(repository["source_snapshot_digest"])
    need(split_repo_ids["DEV"] and split_repo_ids["HOLDOUT"], "both DEV and HOLDOUT splits must contain a repository")
    need(not split_repo_ids["DEV"] & split_repo_ids["HOLDOUT"], "repository identity crosses DEV/HOLDOUT")
    need(not split_repo_digests["DEV"] & split_repo_digests["HOLDOUT"], "repository content digest crosses DEV/HOLDOUT")
    need(not split_snapshot_digests["DEV"] & split_snapshot_digests["HOLDOUT"], "source snapshot digest crosses DEV/HOLDOUT")

    queries = dataset["queries"]
    need(isinstance(queries, list) and 2 <= len(queries) <= MAX_QUERY_COUNT, "dataset query inventory is outside its bound")
    query_by_id: dict[str, dict[str, Any]] = {}
    normalized_by_split: dict[str, set[str]] = {"DEV": set(), "HOLDOUT": set()}
    for query in queries:
        exact_fields(query, {"id", "repository_id", "query", "intent", "ks", "relevant_spans", "task_ids", "labels_reviewed_at_utc"}, set(), "query assignment")
        ident = safe_id(query["id"], "query id")
        need(ident not in query_by_id, "duplicate query id")
        repo_id = safe_id(query["repository_id"], "query repository id")
        need(repo_id in repo_by_id, "query refers to an undeclared repository")
        repo = repo_by_id[repo_id]
        text = query["query"]
        need(isinstance(text, str) and 1 <= len(text.encode("utf-8")) <= 256 and "\x00" not in text and text.strip(), "query text is invalid")
        need(query["intent"] in ("IDENTIFIER", "ERROR", "FEATURE", "REFACTOR"), "query intent is invalid")
        ks = query["ks"]
        need(isinstance(ks, list) and 1 <= len(ks) <= 8 and all(is_int(k) and 1 <= k <= MAX_PAGE_SIZE for k in ks), "query K values are invalid")
        need(ks == sorted(set(ks)), "query K values must be sorted and unique")
        labels = query["relevant_spans"]
        need(isinstance(labels, list) and 1 <= len(labels) <= MAX_SPANS_PER_QUERY, "each retrieval query needs one to 256 reviewed relevant spans")
        paths = {item["path"]: item for item in repo["files"]}
        seen_spans: set[tuple[str, int, int]] = set()
        intervals_by_path: dict[str, list[tuple[int, int]]] = {}
        for label in labels:
            exact_fields(label, {"path", "file_sha256", "byte_start", "byte_end", "span_sha256"}, set(), "reviewed relevant span")
            path = safe_relative_path(label["path"], "relevant span path")
            need(path in paths and paths[path]["authorized"], "relevant span must belong to an authorized source file")
            file_entry = paths[path]
            need(label["file_sha256"] == file_entry["sha256"], "relevant span file digest differs from frozen source")
            start, end = label["byte_start"], label["byte_end"]
            need(is_int(start) and is_int(end) and 0 <= start < end <= file_entry["size_bytes"], "relevant span byte interval is invalid")
            need(valid_digest(label["span_sha256"]), "relevant span digest is invalid")
            key = (path, start, end)
            need(key not in seen_spans, "duplicate relevant span")
            need(not any(start < prior_end and prior_start < end for prior_start, prior_end in intervals_by_path.get(path, [])), "relevant spans for one query must not overlap")
            seen_spans.add(key)
            intervals_by_path.setdefault(path, []).append((start, end))
            span = read_exact(root_paths[repo_id][path], start, end)
            need(digest_bytes(span) == label["span_sha256"], "relevant span bytes differ from the reviewed label digest")
        assignments = query["task_ids"]
        need(isinstance(assignments, dict) and set(assignments) == set(condition_by_id), "each query needs one frozen task assignment per condition")
        for condition_id, task_id in assignments.items():
            safe_id(task_id, f"task id for {condition_id}")
        reviewed_at = utc_time(query["labels_reviewed_at_utc"], "label review time")
        need(utc_time(repo["snapshot_at_utc"], "snapshot time") <= reviewed_at <= frozen_at, "labels must be reviewed after source capture and before dataset freeze")
        split = repo["split"]
        normalized = " ".join(text.casefold().split())
        need(normalized not in normalized_by_split[split], "duplicate normalized query within a split")
        normalized_by_split[split].add(normalized)
        query["_max_k"] = max(ks)
        query["_split"] = split
        query_by_id[ident] = query
    need({query["_split"] for query in query_by_id.values()} == {"DEV", "HOLDOUT"}, "both DEV and HOLDOUT splits must contain labeled queries")
    need(not normalized_by_split["DEV"] & normalized_by_split["HOLDOUT"], "normalized query crosses DEV/HOLDOUT")

    dataset["_digest"] = actual_digest
    dataset["_frozen_at"] = frozen_at
    dataset["_conditions_by_id"] = condition_by_id
    dataset["_repositories_by_id"] = repo_by_id
    dataset["_queries_by_id"] = query_by_id
    dataset["_paths_by_repo"] = root_paths
    dataset["_source_roots"] = roots
    return dataset


def validate_partition(result: dict[str, Any], repo: dict[str, Any], args: dict[str, Any]) -> tuple[bool, list[dict[str, Any]]]:
    files = [item for item in repo["files"] if item["authorized"]]
    if "partition" not in result:
        need(len(files) <= 128 and sum(item["size_bytes"] for item in files) <= 4 << 20 and args.get("partition", 0) == 0, "a large or nonzero-partition search is missing partition evidence")
        return False, files
    need(result["partition"] is not None, "partition evidence cannot be null")

    evidence = result["partition"]
    exact_fields(evidence, {"version", "plan_digest", "authorized_source_set", "authorized_documents", "authorized_bytes", "selected", "partitions", "scope"}, {"next_partition"}, "partition evidence")
    need(evidence["version"] == PARTITION_VERSION and evidence["scope"] == PARTITION_SCOPE, "unsupported partition protocol")
    need(is_int(evidence["authorized_documents"]) and is_int(evidence["authorized_bytes"]), "partition inventory counts are invalid")
    need(valid_digest(evidence["plan_digest"]) and evidence["authorized_source_set"] == repo["_authorized_source_set"], "partition plan source binding differs")
    need(evidence["authorized_documents"] == len(files) and evidence["authorized_bytes"] == sum(item["size_bytes"] for item in files), "partition authorized inventory differs")
    groups: list[list[dict[str, Any]]] = []
    for item in files:
        if not groups or len(groups[-1]) >= 128 or item["size_bytes"] > (4 << 20) - sum(entry["size_bytes"] for entry in groups[-1]):
            groups.append([])
        groups[-1].append(item)
    partitions = evidence["partitions"]
    need(isinstance(partitions, list) and len(partitions) == len(groups), "partition list does not cover the authorized source set")
    for part in partitions:
        exact_fields(part, {"ordinal", "digest", "documents", "source_bytes", "first_path", "last_path"}, set(), "partition entry")
        need(is_int(part["ordinal"]) and is_int(part["documents"]) and is_int(part["source_bytes"]), "partition entry counts are invalid")
        need(valid_digest(part["digest"]) and isinstance(part["first_path"], str) and isinstance(part["last_path"], str), "partition entry identity is invalid")
    selected_ordinal = args.get("partition", 0)
    need(is_int(selected_ordinal) and 0 <= selected_ordinal < len(groups), "report partition ordinal is invalid")
    expected_parts: list[dict[str, Any]] = []
    for ordinal, group in enumerate(groups):
        refs = [{"Path": item["path"], "Digest": item["sha256"]} for item in group]
        part_digest = viber_digest({
            "Version": PARTITION_VERSION,
            "Binding": {"candidate": repo["source_snapshot_digest"], "policy": repo["policy_digest"]},
            "SourceSet": repo["_authorized_source_set"],
            "Ordinal": ordinal,
            "Sources": refs,
        })
        expected_parts.append({
            "ordinal": ordinal,
            "digest": part_digest,
            "documents": len(group),
            "source_bytes": sum(item["size_bytes"] for item in group),
            "first_path": group[0]["path"] if group else "",
            "last_path": group[-1]["path"] if group else "",
        })
    need(partitions == expected_parts, "partition boundaries or source digests differ from frozen inventory")
    selected = evidence["selected"]
    exact_fields(selected, {"ordinal", "digest", "documents", "source_bytes", "first_path", "last_path"}, set(), "selected partition")
    need(is_int(selected["ordinal"]) and is_int(selected["documents"]) and is_int(selected["source_bytes"]), "selected partition counts are invalid")
    need(selected == expected_parts[selected_ordinal], "selected partition differs from the report arguments")
    expected_next = selected_ordinal + 1 if selected_ordinal + 1 < len(groups) else None
    if "next_partition" in evidence:
        need(is_int(evidence["next_partition"]), "next partition ordinal is invalid")
    need(evidence.get("next_partition") == expected_next, "partition continuation differs from the frozen plan")
    plan_digest = viber_digest({
        "version": PARTITION_VERSION,
        "binding": {"candidate": repo["source_snapshot_digest"], "policy": repo["policy_digest"]},
        "source_set": repo["_authorized_source_set"],
        "documents": len(files),
        "source_bytes": sum(item["size_bytes"] for item in files),
        "partitions": expected_parts,
        "digest": "",
    })
    need(plan_digest == evidence["plan_digest"], "partition plan digest differs")
    return True, groups[selected_ordinal]


def validate_runtime_reply(raw_reply: str, args_raw: str, query: dict[str, Any], repo: dict[str, Any], source_paths: dict[str, Path]) -> dict[str, Any]:
    need(len(raw_reply.encode("utf-8")) <= MAX_REPLY_BYTES, "fs_search reply exceeds its size bound")
    parsed = parse_json(raw_reply.encode("utf-8"), MAX_REPLY_BYTES, "fs_search reply")
    call_args = parse_json(args_raw.encode("utf-8"), 8192, "fs_search call arguments")
    exact_fields(call_args, {"query", "mode"}, {"intent", "candidate_digest", "limit", "partition", "cursor"}, "fs_search call arguments")
    effective_intent = call_args.get("intent", "IDENTIFIER")
    need(call_args["mode"] == "ranked" and call_args["query"] == query["query"] and effective_intent == query["intent"], "fs_search arguments differ from the frozen query")
    need("candidate_digest" not in call_args or call_args["candidate_digest"] == repo["source_snapshot_digest"], "fs_search candidate argument differs from the frozen source snapshot")
    limit = call_args.get("limit", 32)
    need(is_int(limit) and query["_max_k"] <= limit <= MAX_PAGE_SIZE, "fs_search page limit does not cover the frozen K values")
    need("offset" not in call_args and call_args.get("cursor", "") == "", "only an initial ranked page can be scored")
    need("partition" not in call_args or (is_int(call_args["partition"]) and 0 <= call_args["partition"] < 128), "fs_search partition argument is invalid")

    if isinstance(parsed, dict) and parsed.get("fallback_used") is True:
        exact_fields(parsed, {"fallback_used", "reason", "literal", "fallback_scope"}, {"requested_partition"}, "ranked fallback reply")
        need(parsed["reason"] == "INDEX_UNAVAILABLE_WITHIN_BOUNDED_PROFILE", "unknown fallback reason")
        need(parsed["fallback_scope"] == "FULL_CAPTURED_POLICY_SCOPE_LITERAL_PAGE; NOT_PARTITION_RANKING", "fallback scope is not the Viber literal fallback")
        return {"status": "FALLBACK", "query_micros": None, "hits": [], "partial_reason": "FALLBACK"}

    exact_fields(parsed, {"retrieval", "coverage", "scope"}, set(), "ranked fs_search reply")
    need(parsed["scope"] == SCOPE, "ranked reply scope differs from Viber's declared scope")
    result = exact_fields(
        parsed["retrieval"],
        {"schema_version", "trust", "index", "query", "intent", "hits", "ranked_pool_total", "ranked_pool_truncated", "query_micros", "fallback_used", "span_policy"},
        {"partition", "cache", "advisory_ranking"},
        "retrieval result",
    )
    need(is_int(result["schema_version"]) and result["schema_version"] == 1 and result["trust"] == TRUST and result["fallback_used"] is False, "retrieval result is not a current ranked hint")
    need(result["query"] == query["query"] and result["intent"] == query["intent"], "retrieval result query differs from frozen assignment")
    need(result["span_policy"] == "MAX_3_NONOVERLAPPING_CHUNKS_PER_FILE; BODY_LITERAL_BEFORE_PATH", "unsupported retrieval span policy")
    need(is_int(result["query_micros"]) and result["query_micros"] >= 0, "runtime query latency is invalid")
    need(is_int(result["ranked_pool_total"]) and 0 <= result["ranked_pool_total"] <= 1024, "ranked pool count is invalid")
    need(isinstance(result["ranked_pool_truncated"], bool), "ranked pool truncation flag is invalid")

    manifest = exact_fields(
        result["index"],
        {"version", "binding", "source_set", "documents", "indexed_documents", "skipped_binary", "chunks", "source_bytes", "sqlite_allocated_bytes", "build_micros"},
        set(),
        "retrieval index manifest",
    )
    need(manifest["version"] == RETRIEVAL_VERSION, "retrieval index version differs")
    binding = exact_fields(manifest["binding"], {"candidate", "policy"}, set(), "retrieval source binding")
    need(binding == {"candidate": repo["source_snapshot_digest"], "policy": repo["policy_digest"]}, "retrieval index is not bound to the frozen candidate and policy")
    need(is_int(manifest["documents"]) and is_int(manifest["source_bytes"]) and valid_digest(manifest["source_set"]), "retrieval index source counts or digest are invalid")

    partitioned, selected_files = validate_partition(result, repo, call_args)
    selected_source_set = viber_digest([{"Path": item["path"], "Digest": item["sha256"]} for item in selected_files])
    need(manifest["source_set"] == selected_source_set, "retrieval index source set differs from frozen files")
    need(manifest["documents"] == len(selected_files), "retrieval index document count differs from its source set")
    need(manifest["source_bytes"] == sum(item["size_bytes"] for item in selected_files), "retrieval index byte count differs from its source set")
    for field in ("indexed_documents", "skipped_binary", "chunks", "sqlite_allocated_bytes", "build_micros"):
        need(is_int(manifest[field]) and manifest[field] >= 0, "retrieval index manifest contains an invalid count")
    need(manifest["indexed_documents"] + manifest["skipped_binary"] == manifest["documents"], "retrieval index document accounting does not balance")

    hits = result["hits"]
    need(isinstance(hits, list) and len(hits) <= limit and len(hits) <= MAX_PAGE_SIZE, "ranked hit page exceeds its declared limit")
    file_index = {item["path"]: item for item in repo["files"]}
    authorized = {item["path"] for item in selected_files}
    seen_hits: set[tuple[str, int, int]] = set()
    checked_hits: list[dict[str, Any]] = []
    for hit in hits:
        exact_fields(hit, {"path", "file_digest", "byte_start", "byte_end", "line", "exact_bytes", "literal_match", "rrf_score_micros", "signals"}, set(), "ranked hit")
        path = safe_relative_path(hit["path"], "ranked hit path")
        need(path in authorized and path in file_index, "ranked hit points outside the selected authorized source")
        source = file_index[path]
        start, end = hit["byte_start"], hit["byte_end"]
        need(is_int(start) and is_int(end) and 0 <= start < end <= source["size_bytes"], "ranked hit byte interval is invalid")
        need(hit["file_digest"] == source["sha256"], "ranked hit file digest differs from frozen source")
        need(is_int(hit["line"]) and hit["line"] >= 1, "ranked hit line number is invalid")
        need(isinstance(hit["exact_bytes"], str), "ranked hit exact bytes are absent")
        try:
            exact = base64.b64decode(hit["exact_bytes"], validate=True)
        except (ValueError, base64.binascii.Error) as exc:
            raise InvalidInput("ranked hit exact bytes are not base64") from exc
        need(len(exact) == end - start and read_exact(source_paths[path], start, end) == exact, "ranked hit bytes do not match their source offsets")
        need(hit["line"] == exact_line_count(source_paths[path], start), "ranked hit line differs from its source offset")
        need(isinstance(hit["literal_match"], bool) and is_int(hit["rrf_score_micros"]) and hit["rrf_score_micros"] >= 0, "ranked hit score fields are invalid")
        need(isinstance(hit["signals"], list), "ranked hit signals must be a list")
        key = (path, start, end)
        need(key not in seen_hits, "ranked result repeats an exact hit")
        seen_hits.add(key)
        checked_hits.append({"path": path, "file_digest": source["sha256"], "start": start, "end": end})

    coverage = exact_fields(parsed["coverage"], {"page_start", "page_end", "total", "next_cursor", "has_more"}, set(), "ranked page coverage")
    need(all(is_int(coverage[field]) for field in ("page_start", "page_end", "total")), "ranked page coverage counts are invalid")
    need(coverage["page_start"] == 0 and coverage["page_end"] == len(hits), "only a complete first page can be scored")
    need(coverage["total"] == result["ranked_pool_total"] and coverage["total"] >= len(hits), "page and ranked pool counts disagree")
    need(isinstance(coverage["has_more"], bool) and isinstance(coverage["next_cursor"], str), "ranked page continuation fields are invalid")
    need(coverage["has_more"] == (len(hits) < coverage["total"]), "page continuation flag disagrees with ranked pool count")
    need(bool(coverage["next_cursor"]) == coverage["has_more"], "page cursor presence disagrees with coverage")
    need(len(hits) >= min(query["_max_k"], result["ranked_pool_total"]), "ranked page is too short for the frozen K values")

    reason = None
    if partitioned:
        reason = "PARTITIONED_SOURCE_NO_GLOBAL_RANK"
    elif result["ranked_pool_truncated"]:
        reason = "RANKED_POOL_TRUNCATED"
    return {
        "status": "RANKED",
        "query_micros": result["query_micros"],
        "hits": checked_hits,
        "partial_reason": reason,
        "full_pool": len(hits) == result["ranked_pool_total"],
    }


def exact_line_count(path: Path, end: int) -> int:
    count = 0
    try:
        with path.open("rb") as stream:
            remaining = end
            while remaining:
                block = stream.read(min(1 << 20, remaining))
                if not block:
                    raise InvalidInput("ranked hit offset exceeds source bytes")
                count += block.count(b"\n")
                remaining -= len(block)
    except OSError as exc:
        raise InvalidInput("ranked hit line could not be checked") from exc
    return count + 1


def read_reports(path: str, dataset: dict[str, Any]) -> tuple[dict[tuple[str, str], dict[str, Any]], dict[str, Any]]:
    report_path = Path(path)
    try:
        info = report_path.stat()
        need(stat.S_ISREG(info.st_mode) and info.st_size <= MAX_REPORT_BYTES, "reports input is not a bounded regular file")
        raw_file = report_path.read_bytes()
    except OSError as exc:
        raise InvalidInput("reports file could not be read") from exc
    need(len(raw_file) <= MAX_REPORT_BYTES, "reports file exceeds its size bound")
    rows: dict[tuple[str, str], dict[str, Any]] = {}
    task_sequences: set[tuple[str, str, int]] = set()
    call_ids: set[tuple[str, str, str]] = set()
    run_id: str | None = None
    line_count = 0
    for line in raw_file.splitlines():
        if not line.strip():
            continue
        line_count += 1
        need(line_count <= MAX_QUERY_COUNT * MAX_CONDITIONS, "reports row count exceeds its bound")
        need(len(line) <= MAX_REPORT_LINE_BYTES, "reports JSONL row exceeds its size bound")
        row = parse_json(line, MAX_REPORT_LINE_BYTES, "reports JSONL row")
        exact_fields(
            row,
            {"schema_version", "kind", "dataset_sha256", "run_id", "condition_id", "query_id", "task_id", "task_seq", "journal_seq", "call_id", "observed_at_utc", "source_revision", "build_sha256", "arguments_json", "arguments_sha256", "fs_search_reply", "reply_sha256", "wall_micros", "usage"},
            set(),
            "reports JSONL row",
        )
        need(is_int(row["schema_version"]) and row["schema_version"] == 1 and row["kind"] == "VIBER_FS_SEARCH_IMPORT_V1", "unsupported fs_search report envelope")
        need(row["dataset_sha256"] == dataset["_digest"], "report is bound to a different frozen dataset")
        row_run = safe_id(row["run_id"], "run id")
        if run_id is None:
            run_id = row_run
        need(run_id == row_run, "reports file mixes run ids")
        condition_id = safe_id(row["condition_id"], "condition id")
        query_id = safe_id(row["query_id"], "query id")
        key = (condition_id, query_id)
        need(condition_id in dataset["_conditions_by_id"] and query_id in dataset["_queries_by_id"], "report refers to an undeclared assignment")
        need(key not in rows, "duplicate report for an assigned query; best-run selection is not accepted")
        condition = dataset["_conditions_by_id"][condition_id]
        query = dataset["_queries_by_id"][query_id]
        repo = dataset["_repositories_by_id"][query["repository_id"]]
        need(row["task_id"] == query["task_ids"][condition_id], "report task id differs from the frozen assignment")
        need(is_int(row["task_seq"]) and row["task_seq"] >= 1 and is_int(row["journal_seq"]) and row["journal_seq"] >= row["task_seq"], "report task and journal cursors must be positive and ordered")
        task_cursor = (condition_id, row["task_id"], row["task_seq"])
        need(task_cursor not in task_sequences, "two query reports claim the same condition/task cursor")
        task_sequences.add(task_cursor)
        call_id = safe_call_id(row["call_id"])
        call_key = (condition_id, row["task_id"], call_id)
        need(call_key not in call_ids, "duplicate fs_search call id")
        call_ids.add(call_key)
        observed_at = utc_time(row["observed_at_utc"], "report observed time")
        need(observed_at >= dataset["_frozen_at"], "report predates the frozen dataset")
        need(row["arguments_sha256"] == digest_bytes(row["arguments_json"].encode("utf-8")) if isinstance(row["arguments_json"], str) else False, "fs_search argument digest differs")
        need(row["reply_sha256"] == digest_bytes(row["fs_search_reply"].encode("utf-8")) if isinstance(row["fs_search_reply"], str) else False, "fs_search reply digest differs")
        need(valid_digest(row["arguments_sha256"]) and valid_digest(row["reply_sha256"]), "report raw-byte digest is invalid")
        need(valid_digest(row["build_sha256"]), "report build digest is invalid")
        usage = exact_fields(row["usage"], {"known", "input_tokens", "output_tokens"}, set(), "model token usage")
        need(isinstance(usage["known"], bool), "usage-known flag must be boolean")
        if usage["known"]:
            need(is_int(usage["input_tokens"]) and usage["input_tokens"] >= 0 and is_int(usage["output_tokens"]) and usage["output_tokens"] >= 0, "known token usage must contain nonnegative counts")
        else:
            need(usage["input_tokens"] is None and usage["output_tokens"] is None, "unknown token usage must use null counts")
        wall = row["wall_micros"]
        need(wall is None or (is_int(wall) and wall >= 0), "wall-clock latency must be a nonnegative integer or null")
        need(isinstance(row["source_revision"], str) and SOURCE_REVISION.fullmatch(row["source_revision"]) is not None, "report source revision must be a full SHA id")
        need(row["build_sha256"] == condition["build_sha256"] and row["source_revision"] == condition["source_revision"], "report build/revision differs from its frozen condition")
        parsed = validate_runtime_reply(row["fs_search_reply"], row["arguments_json"], query, repo, dataset["_paths_by_repo"][query["repository_id"]])
        rows[key] = {"envelope": row, "parsed": parsed, "query": query, "repo": repo}
    return rows, {"sha256": digest_bytes(raw_file), "bytes": len(raw_file), "rows": len(rows), "run_id": run_id}


def overlap(label: dict[str, Any], hit: dict[str, Any]) -> bool:
    return label["path"] == hit["path"] and label["file_sha256"] == hit["file_digest"] and label["byte_start"] < hit["end"] and hit["start"] < label["byte_end"]


def p95_nearest_rank(values: list[int]) -> int | None:
    if not values:
        return None
    ordered = sorted(values)
    index = math.ceil(0.95 * len(ordered)) - 1
    return ordered[max(0, index)]


def split_metrics(condition_id: str, split: str, queries: list[dict[str, Any]], rows: dict[tuple[str, str], dict[str, Any]]) -> dict[str, Any]:
    unknown: dict[str, int] = {}
    scored: list[dict[str, Any]] = []
    mrr_scored: list[dict[str, Any]] = []
    latency: list[int] = []
    wall_latency: list[int] = []
    token_input = 0
    token_output = 0
    known_usage = 0
    unknown_usage = 0
    input_token_samples: list[int] = []
    output_token_samples: list[int] = []
    combined_token_samples: list[int] = []
    fallback_count = 0
    partial_count = 0
    missing_count = 0
    ranked_count = 0
    for query in queries:
        entry = rows.get((condition_id, query["id"]))
        if entry is None:
            missing_count += 1
            unknown["MISSING_REPORT"] = unknown.get("MISSING_REPORT", 0) + 1
            continue
        envelope = entry["envelope"]
        parsed = entry["parsed"]
        if envelope["usage"]["known"]:
            token_input += envelope["usage"]["input_tokens"]
            token_output += envelope["usage"]["output_tokens"]
            known_usage += 1
            input_token_samples.append(envelope["usage"]["input_tokens"])
            output_token_samples.append(envelope["usage"]["output_tokens"])
            combined_token_samples.append(envelope["usage"]["input_tokens"] + envelope["usage"]["output_tokens"])
        else:
            unknown_usage += 1
        if envelope["wall_micros"] is not None:
            wall_latency.append(envelope["wall_micros"])
        if parsed["status"] == "FALLBACK":
            fallback_count += 1
            unknown["FALLBACK"] = unknown.get("FALLBACK", 0) + 1
            continue
        ranked_count += 1
        latency.append(parsed["query_micros"])
        if parsed["partial_reason"] is not None:
            partial_count += 1
            unknown[parsed["partial_reason"]] = unknown.get(parsed["partial_reason"], 0) + 1
            continue
        scored.append({"query": query, "hits": parsed["hits"]})
        if parsed["full_pool"]:
            mrr_scored.append({"query": query, "hits": parsed["hits"]})

    ks = sorted({k for query in queries for k in query["ks"]})
    recalls: dict[str, Any] = {}
    for k in ks:
        found_total = 0
        label_total = 0
        query_recall_sum = 0.0
        recall_queries = 0
        for item in scored:
            if k not in item["query"]["ks"]:
                continue
            labels = item["query"]["relevant_spans"]
            top_hits = item["hits"][:k]
            found = sum(1 for label in labels if any(overlap(label, hit) for hit in top_hits))
            found_total += found
            label_total += len(labels)
            query_recall_sum += found / len(labels)
            recall_queries += 1
        recalls[str(k)] = {
            "relevant_spans_found": found_total,
            "relevant_spans_total": label_total,
            "micro_recall": round(found_total / label_total, 6) if label_total else None,
            "macro_query_recall": round(query_recall_sum / recall_queries, 6) if recall_queries else None,
            "query_denominator": recall_queries,
        }

    reciprocal_rank_micros = 0
    for item in mrr_scored:
        first_rank = None
        for rank, hit in enumerate(item["hits"], start=1):
            if any(overlap(label, hit) for label in item["query"]["relevant_spans"]):
                first_rank = rank
                break
        if first_rank is not None:
            reciprocal_rank_micros += 1_000_000 // first_rank
    mrr_denominator = len(mrr_scored)
    mrr = round(reciprocal_rank_micros / mrr_denominator / 1_000_000, 6) if mrr_denominator else None
    assigned = len(queries)
    present = assigned - missing_count
    return {
        "split": split,
        "assigned_queries": assigned,
        "reports_present": present,
        "ranked_reports": ranked_count,
        "fallback_reports": fallback_count,
        "missing_reports": missing_count,
        "partial_coverage_reports": partial_count,
        "metric_denominator_queries": len(scored),
        "known_coverage_fraction": round(len(scored) / assigned, 6) if assigned else None,
        "mrr_incomplete_rank_pages": len(scored) - len(mrr_scored),
        "unknown_coverage_by_reason": dict(sorted(unknown.items())),
        "recall_at_k": recalls,
        "mrr": {"reciprocal_rank_sum_micros": reciprocal_rank_micros, "query_denominator": mrr_denominator, "value": mrr},
        "retrieval_query_latency_micros": {"sample_count": len(latency), "p95_nearest_rank": p95_nearest_rank(latency)},
        "wrapper_wall_latency_micros": {"sample_count": len(wall_latency), "p95_nearest_rank": p95_nearest_rank(wall_latency)},
        "model_reported_tokens": {
            "known_usage_reports": known_usage,
            "unknown_usage_reports": unknown_usage,
            "known_input_token_total": token_input,
            "known_output_token_total": token_output,
            "input_token_p95_nearest_rank": {"sample_count": len(input_token_samples), "value": p95_nearest_rank(input_token_samples)},
            "output_token_p95_nearest_rank": {"sample_count": len(output_token_samples), "value": p95_nearest_rank(output_token_samples)},
            "combined_token_p95_nearest_rank": {"sample_count": len(combined_token_samples), "value": p95_nearest_rank(combined_token_samples)},
        },
    }


def evaluate(dataset: dict[str, Any], rows: dict[tuple[str, str], dict[str, Any]], report_info: dict[str, Any]) -> dict[str, Any]:
    conditions = []
    queries_by_split = {
        split: [query for query in dataset["_queries_by_id"].values() if query["_split"] == split]
        for split in ("DEV", "HOLDOUT")
    }
    for condition_id, condition in dataset["_conditions_by_id"].items():
        conditions.append({
            "id": condition_id,
            "label": condition["label"],
            "build_sha256": condition["build_sha256"],
            "source_revision": condition["source_revision"],
            "dev": split_metrics(condition_id, "DEV", queries_by_split["DEV"], rows),
            "holdout": split_metrics(condition_id, "HOLDOUT", queries_by_split["HOLDOUT"], rows),
        })
    return {
        "schema_version": 1,
        "kind": "VIBER_RETRIEVAL_METRICS_V1",
        "dataset_id": dataset["dataset_id"],
        "dataset_sha256": dataset["_digest"],
        "protocol_digest": dataset["protocol_digest"],
        "label_provenance_digest": dataset["label_provenance_digest"],
        "reports_sha256": report_info["sha256"],
        "reports_bytes": report_info["bytes"],
        "reports_present": report_info["rows"],
        "run_id": report_info["run_id"],
        "conditions": conditions,
        "trust": "IMPORTED_RUNTIME_REPORTS_UNATTESTED; HUMAN_LABELS_ARE_DECLARED_INPUTS; METRICS_ARE_DESCRIPTIVE_ONLY",
        "release_evidence": False,
        "adoption_allowed": False,
        "quality_claim": False,
    }


def self_test() -> dict[str, Any]:
    """Exercise digest binding, exact spans, metric formulas, and missing rows."""
    import tempfile

    now = "2026-01-02T00:00:00Z"
    revision = "a" * 40
    build = digest_bytes(b"synthetic test binary")
    protocol = digest_bytes(b"synthetic protocol")
    label_provenance = digest_bytes(b"synthetic human-label receipt")
    with tempfile.TemporaryDirectory(prefix="viber-retrieval-selftest-") as temporary:
        root = Path(temporary)
        dev_root = root / "dev"
        holdout_root = root / "holdout"
        dev_root.mkdir()
        holdout_root.mkdir()
        dev_bytes = b"def find_item():\n    return 1\n"
        holdout_bytes = b"def load_record():\n    return 2\n"
        (dev_root / "src.py").write_bytes(dev_bytes)
        (holdout_root / "src.py").write_bytes(holdout_bytes)
        def repo(ident: str, split: str, body: bytes) -> dict[str, Any]:
            files = [{"path": "src.py", "sha256": digest_bytes(body), "size_bytes": len(body), "authorized": True}]
            return {
                "id": ident, "split": split, "repository_digest": file_inventory_digest(files),
                "source_snapshot_digest": digest_bytes((ident + " snapshot").encode()),
                "policy_digest": digest_bytes(b"synthetic policy"),
                "history_cutoff_utc": "2025-12-31T00:00:00Z", "snapshot_at_utc": "2026-01-01T00:00:00Z", "files": files,
            }
        dev_repo = repo("dev-repo", "DEV", dev_bytes)
        hold_repo = repo("holdout-repo", "HOLDOUT", holdout_bytes)
        start = dev_bytes.index(b"find_item")
        span = dev_bytes[start:start + len(b"find_item")]
        condition = {"id": "current", "build_sha256": build, "source_revision": revision, "label": "synthetic fixture only"}
        dataset_obj = {
            "schema_version": 1, "kind": "VIBER_RETRIEVAL_DATASET_V1", "dataset_id": "synthetic-fixture",
            "protocol_digest": protocol, "label_provenance_digest": label_provenance, "frozen_at_utc": now,
            "conditions": [condition], "repositories": [dev_repo, hold_repo],
            "queries": [
                {"id": "dev-find", "repository_id": "dev-repo", "query": "find_item", "intent": "IDENTIFIER", "ks": [1, 3],
                 "relevant_spans": [{"path": "src.py", "file_sha256": digest_bytes(dev_bytes), "byte_start": start, "byte_end": start + len(span), "span_sha256": digest_bytes(span)}],
                 "task_ids": {"current": "task-dev"}, "labels_reviewed_at_utc": now},
                {"id": "hold-load", "repository_id": "holdout-repo", "query": "load_record", "intent": "IDENTIFIER", "ks": [1, 3],
                 "relevant_spans": [{"path": "src.py", "file_sha256": digest_bytes(holdout_bytes), "byte_start": 4, "byte_end": 15, "span_sha256": digest_bytes(holdout_bytes[4:15])}],
                 "task_ids": {"current": "task-hold"}, "labels_reviewed_at_utc": now},
            ],
        }
        dataset_raw = canonical(dataset_obj)
        dataset = validate_dataset(dataset_raw, digest_bytes(dataset_raw), {"dev-repo": str(dev_root), "holdout-repo": str(holdout_root)})
        expect_rejects(
            lambda: validate_dataset(dataset_raw, digest_bytes(b"wrong pinned bytes"), {"dev-repo": str(dev_root), "holdout-repo": str(holdout_root)}),
            "self-test did not reject a mismatched frozen dataset digest",
        )
        source_set = dev_repo["_authorized_source_set"] if "_authorized_source_set" in dev_repo else source_set_digest(dev_repo["files"])
        reply_obj = {
            "retrieval": {
                "schema_version": 1, "trust": TRUST,
                "index": {"version": RETRIEVAL_VERSION, "binding": {"candidate": dev_repo["source_snapshot_digest"], "policy": dev_repo["policy_digest"]},
                          "source_set": source_set, "documents": 1, "indexed_documents": 1, "skipped_binary": 0,
                          "chunks": 1, "source_bytes": len(dev_bytes), "sqlite_allocated_bytes": 4096, "build_micros": 10},
                "query": "find_item", "intent": "IDENTIFIER",
                "hits": [{"path": "src.py", "file_digest": digest_bytes(dev_bytes), "byte_start": start, "byte_end": start + len(span),
                          "line": 1, "exact_bytes": base64.b64encode(span).decode("ascii"), "literal_match": True,
                          "rrf_score_micros": 1000000, "signals": [{"kind": "BODY_LITERAL", "rank": 1}]}],
                "ranked_pool_total": 1, "ranked_pool_truncated": False, "query_micros": 125,
                "fallback_used": False, "span_policy": "MAX_3_NONOVERLAPPING_CHUNKS_PER_FILE; BODY_LITERAL_BEFORE_PATH",
            },
            "coverage": {"page_start": 0, "page_end": 1, "total": 1, "next_cursor": "", "has_more": False},
            "scope": SCOPE,
        }
        reply = json.dumps(reply_obj, separators=(",", ":"))
        arguments = json.dumps({"query": "find_item", "mode": "ranked", "intent": "IDENTIFIER", "candidate_digest": dev_repo["source_snapshot_digest"], "limit": 3}, separators=(",", ":"))
        forged_reply_obj = json.loads(reply)
        forged_reply_obj["retrieval"]["hits"][0]["file_digest"] = digest_bytes(b"not the source")
        forged_reply = json.dumps(forged_reply_obj, separators=(",", ":"))
        expect_rejects(
            lambda: validate_runtime_reply(forged_reply, arguments, dataset["_queries_by_id"]["dev-find"], dev_repo, dataset["_paths_by_repo"]["dev-repo"]),
            "self-test did not reject a forged source hit",
        )
        report_row = {
            "schema_version": 1, "kind": "VIBER_FS_SEARCH_IMPORT_V1", "dataset_sha256": dataset["_digest"],
            "run_id": "self-test", "condition_id": "current", "query_id": "dev-find", "task_id": "task-dev",
            "task_seq": 4, "journal_seq": 9, "call_id": "call-one", "observed_at_utc": "2026-01-03T00:00:00Z",
            "source_revision": revision, "build_sha256": build,
            "arguments_json": arguments, "arguments_sha256": digest_bytes(arguments.encode()),
            "fs_search_reply": reply, "reply_sha256": digest_bytes(reply.encode()), "wall_micros": 900,
            "usage": {"known": True, "input_tokens": 250, "output_tokens": 20},
        }
        # v1 envelope requires the two build identity fields explicitly.
        # Ensure its schema validator recognizes them as required, rather than silently defaulting.
        reports_raw = (json.dumps(report_row, separators=(",", ":")) + "\n").encode()
        incomplete_obj = json.loads(reply)
        def synthetic_hit(start_at: int, end_at: int, line_number: int) -> dict[str, Any]:
            exact_bytes = dev_bytes[start_at:end_at]
            return {
                "path": "src.py", "file_digest": digest_bytes(dev_bytes), "byte_start": start_at, "byte_end": end_at,
                "line": line_number, "exact_bytes": base64.b64encode(exact_bytes).decode("ascii"), "literal_match": False,
                "rrf_score_micros": 500000, "signals": [{"kind": "BM25", "rank": 2}],
            }
        incomplete_obj["retrieval"]["hits"] = [
            incomplete_obj["retrieval"]["hits"][0],
            synthetic_hit(0, 3, 1),
            synthetic_hit(dev_bytes.index(b"return"), dev_bytes.index(b"return") + len(b"return"), 2),
        ]
        incomplete_obj["retrieval"]["ranked_pool_total"] = 4
        incomplete_obj["coverage"] = {"page_start": 0, "page_end": 3, "total": 4, "next_cursor": "opaque-cursor", "has_more": True}
        incomplete_reply = json.dumps(incomplete_obj, separators=(",", ":"))
        incomplete_row = dict(report_row)
        incomplete_row.update({"task_seq": 5, "journal_seq": 10, "call_id": "call-two", "fs_search_reply": incomplete_reply, "reply_sha256": digest_bytes(incomplete_reply.encode())})
        incomplete_raw = (json.dumps(incomplete_row, separators=(",", ":")) + "\n").encode()
        incomplete_rows, incomplete_info = read_reports_bytes_for_test(incomplete_raw, dataset)
        incomplete_metrics = evaluate(dataset, incomplete_rows, incomplete_info)["conditions"][0]["dev"]
        need(incomplete_metrics["metric_denominator_queries"] == 1 and incomplete_metrics["recall_at_k"]["1"]["micro_recall"] == 1.0, "self-test expected exact first-page Recall@K")
        need(incomplete_metrics["mrr"]["query_denominator"] == 0 and incomplete_metrics["mrr_incomplete_rank_pages"] == 1, "self-test must not call a partial pool a complete MRR observation")
        rows, info = read_reports_bytes_for_test(reports_raw, dataset)
        expect_rejects(
            lambda: read_reports_bytes_for_test(reports_raw + reports_raw, dataset),
            "self-test did not reject a duplicate query assignment",
        )
        metrics = evaluate(dataset, rows, info)
        condition_metrics = metrics["conditions"][0]
        need(condition_metrics["dev"]["mrr"]["value"] == 1.0, "self-test expected MRR=1.0")
        need(condition_metrics["dev"]["recall_at_k"]["1"]["micro_recall"] == 1.0, "self-test expected Recall@1=1.0")
        need(condition_metrics["holdout"]["missing_reports"] == 1 and condition_metrics["holdout"]["metric_denominator_queries"] == 0, "self-test expected missing HOLDOUT coverage to remain visible")
        need(condition_metrics["dev"]["model_reported_tokens"]["combined_token_p95_nearest_rank"]["value"] == 270, "self-test expected a known combined-token p95 sample")
        need(metrics["release_evidence"] is False and metrics["adoption_allowed"] is False, "self-test quality gates must remain false")
        return {"self_test": "PASS", "scope": "SYNTHETIC_FIXTURE_ONLY", "checks": 10}


def expect_rejects(operation: Any, message: str) -> None:
    try:
        operation()
    except InvalidInput:
        return
    raise InvalidInput(message)


def read_reports_bytes_for_test(raw_file: bytes, dataset: dict[str, Any]) -> tuple[dict[tuple[str, str], dict[str, Any]], dict[str, Any]]:
    """In-memory equivalent used only by --self-test."""
    import tempfile

    with tempfile.TemporaryDirectory(prefix="viber-retrieval-reports-") as temporary:
        report_path = Path(temporary) / "reports.jsonl"
        report_path.write_bytes(raw_file)
        return read_reports(str(report_path), dataset)


def parse_root_argument(value: str) -> tuple[str, str]:
    need("=" in value, "source root must be REPOSITORY_ID=PATH")
    ident, path = value.split("=", 1)
    safe_id(ident, "source-root repository id")
    need(bool(path), "source-root path is empty")
    return ident, path


def read_bounded_file(path_value: str, maximum: int, label: str) -> bytes:
    path = Path(path_value)
    try:
        info = path.stat()
        need(stat.S_ISREG(info.st_mode) and 0 <= info.st_size <= maximum, f"{label} is not a bounded regular file")
        raw = path.read_bytes()
    except OSError as exc:
        raise InvalidInput(f"{label} could not be read") from exc
    need(len(raw) <= maximum, f"{label} exceeds its size bound")
    return raw


def revalidate_source_roots(dataset: dict[str, Any]) -> None:
    for repository_id, repository in dataset["_repositories_by_id"].items():
        current = scan_source_root(dataset["_source_roots"][repository_id], repository)
        need(current == dataset["_paths_by_repo"][repository_id], "source root identity changed during scoring")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", help="frozen human-labeled dataset manifest JSON")
    parser.add_argument("--dataset-sha256", help="externally pinned SHA-256 of the exact dataset file bytes")
    parser.add_argument("--source-root", action="append", default=[], metavar="REPOSITORY_ID=PATH", help="materialized source snapshot root; repeat once per repository")
    parser.add_argument("--reports", help="JSONL imported fs_search reports; exact raw tool reply content is retained per row")
    parser.add_argument("--output", help="new metrics JSON file; omitted means stdout")
    parser.add_argument("--self-test", action="store_true", help="run only the synthetic bounded harness checks")
    args = parser.parse_args(argv)
    try:
        if args.self_test:
            if any((args.dataset, args.dataset_sha256, args.source_root, args.reports, args.output)):
                raise InvalidInput("--self-test cannot be combined with benchmark inputs")
            result = self_test()
        else:
            need(bool(args.dataset and args.dataset_sha256 and args.reports), "--dataset, --dataset-sha256, and --reports are required")
            roots: dict[str, str] = {}
            for raw_root in args.source_root:
                ident, path = parse_root_argument(raw_root)
                need(ident not in roots, "duplicate --source-root repository id")
                roots[ident] = path
            dataset_raw = read_bounded_file(args.dataset, MAX_DATASET_BYTES, "dataset manifest")
            dataset = validate_dataset(dataset_raw, args.dataset_sha256, roots)
            rows, report_info = read_reports(args.reports, dataset)
            revalidate_source_roots(dataset)
            result = evaluate(dataset, rows, report_info)
        output = json.dumps(result, ensure_ascii=False, indent=2, allow_nan=False) + "\n"
        if args.output:
            destination = Path(args.output)
            try:
                with destination.open("x", encoding="utf-8", newline="\n") as stream:
                    stream.write(output)
            except FileExistsError as exc:
                raise InvalidInput("metrics output must be a new file") from exc
        else:
            sys.stdout.write(output)
        return 0
    except (InvalidInput, OSError) as exc:
        sys.stderr.write(f"retrieval-evaluation: {exc}\n")
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
