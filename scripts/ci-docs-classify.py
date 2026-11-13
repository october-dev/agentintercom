#!/usr/bin/env python3
"""Fail-closed classifier for the CI documentation fast path (issue #188).

Decides whether every change in a pull request is confined to an explicit,
small allowlist of prose-only Markdown files. Anything else -- code,
workflows, scripts, specs, schemas, manifests, examples, dependencies,
unknown statuses, renames crossing the allowlist boundary, empty changesets,
or any classification error -- resolves to ``docs_only=false`` so the full
CI checks run.

The allowlist is deliberately exact paths plus one narrow release-notes glob:

* ``README.md``, ``CONTRIBUTING.md``, ``CODE_OF_CONDUCT.md``, ``SECURITY.md``,
  ``CHANGELOG.md``, ``ROADMAP.md`` (root contributor/community prose)
* ``docs/releases.md`` and ``docs/release-notes-*.md`` (release prose)

Everything under ``docs/`` that describes protocol surface, dependencies,
operations, or architecture is intentionally excluded: those PRs still run
the full checks.

Usage (CI, pull_request events only)::

    python3 scripts/ci-docs-classify.py --base <base-sha> --head <head-sha>

writes ``docs_only``, ``docs_files`` (JSON list of changed ``.md`` files
present at head, for the link checker) and ``reason`` to ``$GITHUB_OUTPUT``
and prints them to stdout. ``--entries-json`` accepts canned diff entries
for the self-test (see ``scripts/ci-docs-fastpath.test.mjs``) instead of
running git.
"""

import json
import os
import subprocess
import sys

ALLOWLIST_EXACT = frozenset(
    {
        "README.md",
        "CONTRIBUTING.md",
        "CODE_OF_CONDUCT.md",
        "SECURITY.md",
        "CHANGELOG.md",
        "ROADMAP.md",
        "docs/releases.md",
    }
)
ALLOWLIST_RELEASE_NOTES_PREFIX = "docs/release-notes-"


def is_allowlisted(path):
    """True only for prose-only Markdown paths on the explicit allowlist."""
    if not path or not isinstance(path, str):
        return False
    if "\\" in path or path.startswith("/") or path != os.path.normpath(path):
        return False
    if path.startswith(".") or ".." in path.split("/"):
        return False
    if path in ALLOWLIST_EXACT:
        return True
    if (
        path.startswith(ALLOWLIST_RELEASE_NOTES_PREFIX)
        and path.endswith(".md")
        and "/" not in path[len(ALLOWLIST_RELEASE_NOTES_PREFIX):]
    ):
        return True
    return False


def classify_entries(entries):
    """Classify canned ``(status, [paths])`` diff entries.

    Returns ``(docs_only, reason, docs_files)``. Every unknown input fails
    closed to ``docs_only=False``.
    """
    if not entries:
        return False, "empty changeset runs full checks", []
    docs_files = []
    for entry in entries:
        if not isinstance(entry, (list, tuple)) or len(entry) != 2:
            return False, "unparseable diff entry runs full checks", []
        status, paths = entry
        if not isinstance(status, str) or not status:
            return False, "unparseable diff status runs full checks", []
        if not isinstance(paths, list) or not paths:
            return False, "unparseable diff paths run full checks", []
        kind = status[0]
        if kind in ("A", "M", "D"):
            if len(paths) != 1:
                return False, "malformed diff entry runs full checks", []
            if not is_allowlisted(paths[0]):
                return False, "non-docs change runs full checks: %s" % paths[0], []
            if kind in ("A", "M") and paths[0] not in docs_files:
                docs_files.append(paths[0])
        elif kind == "R":
            # Both sides of a rename must stay inside the allowlist.
            if len(paths) != 2:
                return False, "malformed rename runs full checks", []
            old, new = paths
            if not is_allowlisted(old) or not is_allowlisted(new):
                return False, "rename crossing docs boundary runs full checks", []
            if new not in docs_files:
                docs_files.append(new)
        else:
            # Type changes, copies, unmerged, unknown, or anything else.
            return False, "unrecognized change runs full checks: %s" % status, []
    return True, "all changes are allowlisted prose docs", sorted(docs_files)


def git_name_status(repo, base, head):
    """Return ``(status, [paths])`` entries from ``git diff --name-status -z``."""
    proc = subprocess.run(
        ["git", "diff", "--name-status", "-z", "%s...%s" % (base, head)],
        cwd=repo or os.getcwd(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError("git diff failed: %s" % proc.stderr.decode("utf-8", "replace").strip())
    tokens = proc.stdout.split(b"\0")
    entries = []
    index = 0
    while index < len(tokens):
        status = tokens[index].decode("utf-8", "strict")
        index += 1
        if status == "":
            continue
        if not tokens[index:]:
            raise RuntimeError("truncated git diff output")
        if status[0] in ("R", "C"):
            if len(tokens) - index < 2:
                raise RuntimeError("truncated git diff output")
            old = tokens[index].decode("utf-8", "strict")
            new = tokens[index + 1].decode("utf-8", "strict")
            index += 2
            entries.append((status, [old, new]))
        else:
            path = tokens[index].decode("utf-8", "strict")
            index += 1
            entries.append((status, [path]))
    return entries


def write_output(docs_only, reason, docs_files):
    line = "docs_only=%s" % ("true" if docs_only else "false")
    files = "docs_files=%s" % json.dumps(sorted(docs_files))
    clean_reason = " ".join(reason.split())
    print(line)
    print(files)
    print("reason=%s" % clean_reason)
    output_path = os.environ.get("GITHUB_OUTPUT")
    if output_path:
        with open(output_path, "a", encoding="utf-8") as handle:
            handle.write(line + "\n")
            handle.write(files + "\n")
            handle.write("reason=%s\n" % clean_reason)


def main(argv):
    base = None
    head = None
    repo = None
    entries_json = None
    index = 0
    while index < len(argv):
        arg = argv[index]
        if arg == "--base" and index + 1 < len(argv):
            base = argv[index + 1]
            index += 2
        elif arg == "--head" and index + 1 < len(argv):
            head = argv[index + 1]
            index += 2
        elif arg == "--repo" and index + 1 < len(argv):
            repo = argv[index + 1]
            index += 2
        elif arg == "--entries-json" and index + 1 < len(argv):
            entries_json = argv[index + 1]
            index += 2
        else:
            print("error: unknown argument: %s" % arg, file=sys.stderr)
            return 2
    try:
        if entries_json is not None:
            entries = json.loads(entries_json)
            if not isinstance(entries, list):
                raise ValueError("entries JSON must be a list")
            docs_only, reason, docs_files = classify_entries(
                [(status, paths) for status, paths in entries]
            )
        elif base and head:
            try:
                entries = git_name_status(repo, base, head)
            except Exception as exc:
                # A diff we cannot compute is not a docs-only diff: run
                # everything rather than failing the whole workflow open.
                write_output(False, "diff unavailable runs full checks: %s" % exc, [])
                return 0
            docs_only, reason, docs_files = classify_entries(entries)
        else:
            print("error: need --base/--head or --entries-json", file=sys.stderr)
            return 2
    except Exception as exc:  # noqa: BLE001 - fail closed with a loud error.
        print("error: classifier failed closed: %s" % exc, file=sys.stderr)
        return 1
    write_output(docs_only, reason, docs_files)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
