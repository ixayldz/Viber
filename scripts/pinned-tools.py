#!/usr/bin/env python3
"""Validate pinned Go tooling without fetching or executing a tool.

The vulnerability database is intentionally live; a tool pin does not freeze
advisories or prove that a source revision passed the vulnerability scan.
"""

import argparse
import json
from pathlib import Path
import re
import sys


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate manifest key")
        result[key] = value
    return result


def read_tools(root):
    raw = (root / "scripts" / "pinned-tools.json").read_bytes()
    if len(raw) > 4096:
        raise ValueError("tool manifest too large")
    tools = json.loads(raw, object_pairs_hook=unique_object)
    if (not isinstance(tools, dict)
            or set(tools) != {"schema_version", "go_version", "govulncheck"}
            or type(tools["schema_version"]) is not int
            or tools["schema_version"] != 1):
        raise ValueError("unsupported tool manifest")
    version = tools["go_version"]
    if not isinstance(version, str) or not re.fullmatch(r"1\.[0-9]+\.[0-9]+", version):
        raise ValueError("invalid Go version")
    module = (root / "go.mod").read_text(encoding="utf-8")
    declared = re.findall(r"(?m)^go ([0-9.]+)\s*$", module)
    if declared != [version]:
        raise ValueError("Go pin differs from go.mod")
    vuln = tools["govulncheck"]
    if (not isinstance(vuln, dict) or set(vuln) != {"module", "version"}
            or vuln["module"] != "golang.org/x/vuln/cmd/govulncheck"
            or not isinstance(vuln["version"], str)
            or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", vuln["version"])):
        raise ValueError("invalid vulnerability tool pin")
    return tools


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--govulncheck", action="store_true", help="print the validated module@version")
    args = parser.parse_args()
    try:
        tools = read_tools(Path(__file__).resolve().parent.parent)
    except (OSError, ValueError, TypeError) as exc:
        print("pinned tooling validation failed: " + str(exc), file=sys.stderr)
        return 1
    if args.govulncheck:
        print(tools["govulncheck"]["module"] + "@" + tools["govulncheck"]["version"])
    else:
        print(json.dumps(tools, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
