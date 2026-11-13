#!/usr/bin/env python3
"""Local Markdown link and anchor checker for the CI docs fast path (#188).

Validates outgoing links from a given set of Markdown files without any
network access: relative file targets must exist inside the repository,
``#anchor`` targets must match a GitHub-style heading slug in the target
document, and reference definitions must resolve. Absolute URLs, ``mailto:``
links, and other URI schemes are skipped (never fetched).

Usage::

    python3 scripts/ci-docs-check.py --files-json '["README.md"]' [--repo DIR]

Exits 0 when every check passes (or there is nothing to check) and 1 with
``file:line: message`` diagnostics otherwise.
"""

import json
import os
import re
import sys

INLINE_LINK = re.compile(r"(!?)\[([^\]]*)\]\(([^)]*)\)")
REFERENCE_USE = re.compile(r"(!?)\[([^\]]+)\]\[([^\]]*)\]")
REFERENCE_DEF = re.compile(r"^[ ]{0,3}\[([^\]]+)\]:[ \t]*(\S+)[ \t]*(\"([^\"]*)\"|'([^']*)'|\(([^)]*)\))?[ \t]*$")
AUTOLINK = re.compile(r"<((?:\./|\../|[A-Za-z0-9])[^\s<>]*)>")
HEADING = re.compile(r"^(#{1,6})\s+(.+?)[ \t]*#*[ \t]*$")
FENCED = re.compile(r"^(```|~~~)")
SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")
HTML_ANCHOR = re.compile(r'<a\s+[^>]*?(?:id|name)\s*=\s*["\']([^"\']+)["\']', re.IGNORECASE)
HTML_TAG = re.compile(r"</?[A-Za-z][A-Za-z0-9]*(?:\s+[^<>]*)?/?>")


def github_slug(heading):
    """Best-effort GitHub heading slug (lowercase, punctuation stripped)."""
    text = re.sub(r"`([^`]*)`", r"\1", heading).strip().lower()
    text = re.sub(r"[^\w\s\-]", "", text, flags=re.UNICODE)
    return re.sub(r"\s+", "-", text)


def document_anchors(lines):
    """Collect anchor slugs defined by headings and explicit HTML anchors."""
    anchors = set()
    counts = {}
    for line in lines:
        match = HEADING.match(line)
        if match:
            slug = github_slug(match.group(2))
            seen = counts.get(slug, 0)
            counts[slug] = seen + 1
            anchors.add(slug if seen == 0 else "%s-%d" % (slug, seen))
        for anchor in HTML_ANCHOR.findall(line):
            anchors.add(anchor)
    return anchors


def blank_spans(text):
    """Blank CommonMark-style code spans, which may cross line breaks.

    A run of N backticks is closed by the next run of exactly N backticks.
    An unmatched run is literal text. Wrongly pairing two literal backticks
    only hides text from validation (lenient), never flags valid docs.
    """
    out = list(text)
    index = 0
    while index < len(text):
        if text[index] != "`":
            index += 1
            continue
        end = index
        while end < len(text) and text[end] == "`":
            end += 1
        run = text[index:end]
        close = text.find(run, end)
        if close == -1:
            index = end
            continue
        for pos in range(index, close + len(run)):
            if out[pos] != "\n":
                out[pos] = " "
        index = close + len(run)
    return "".join(out)


def strip_code(lines):
    """Blank fenced blocks and code spans so they are never link-checked."""
    out = []
    fence = None
    for line in lines:
        match = FENCED.match(line.lstrip())
        if match:
            marker = match.group(1)
            if fence is None:
                fence = marker
            elif fence == marker:
                fence = None
            out.append("")
            continue
        out.append("" if fence else line)
    return blank_spans("\n".join(out)).split("\n")


def split_target(raw):
    """Split a raw link destination into (path, anchor)."""
    raw = raw.strip()
    if raw.startswith("<") and raw.endswith(">") and len(raw) >= 2:
        raw = raw[1:-1].strip()
    else:
        raw = raw.split()[0] if raw.split() else ""
        if len(raw) >= 2 and raw[0] == raw[-1] and raw[0] in ("'", '"'):
            raw = raw[1:-1]
    if "#" in raw:
        path, anchor = raw.split("#", 1)
        return path.strip(), anchor.strip()
    return raw, ""


def check_file(repo, relpath, cache):
    """Return a list of ``relpath:line: message`` errors for one document."""
    errors = []
    full = os.path.join(repo, relpath)
    try:
        with open(full, "r", encoding="utf-8") as handle:
            lines = handle.read().splitlines()
    except (OSError, UnicodeDecodeError) as exc:
        return ["%s: cannot read file: %s" % (relpath, exc)]
    visible = strip_code(lines)
    definitions = {}
    for line in visible:
        match = REFERENCE_DEF.match(line)
        if match:
            key = " ".join(match.group(1).split()).lower()
            definitions.setdefault(key, match.group(2))

    def resolve(path, anchor, lineno, note):
        if not path:
            if anchor and anchor not in cache[relpath]:
                errors.append("%s:%d: unknown anchor '#%s' %s" % (relpath, lineno, anchor, note))
            return
        if SCHEME.match(path):
            return  # Remote or special schemes are never fetched.
        if path.startswith("/"):
            target = os.path.normpath(os.path.join(repo, path.lstrip("/")))
        else:
            target = os.path.normpath(os.path.join(repo, os.path.dirname(relpath), path))
        if target != repo and not target.startswith(repo + os.sep):
            errors.append("%s:%d: link escapes repository: %s" % (relpath, lineno, path))
            return
        if not os.path.exists(target):
            errors.append("%s:%d: missing link target: %s" % (relpath, lineno, path))
            return
        if anchor and os.path.isfile(target) and target.endswith(".md"):
            target_rel = os.path.relpath(target, repo)
            if target_rel not in cache:
                try:
                    with open(target, "r", encoding="utf-8") as handle:
                        cache[target_rel] = document_anchors(handle.read().splitlines())
                except (OSError, UnicodeDecodeError):
                    errors.append("%s:%d: cannot read link target: %s" % (relpath, lineno, path))
                    return
            if anchor not in cache[target_rel]:
                errors.append(
                    "%s:%d: unknown anchor '#%s' in %s" % (relpath, lineno, anchor, path)
                )

    for lineno, line in enumerate(visible, 1):
        if REFERENCE_DEF.match(line):
            continue
        scan = HTML_TAG.sub("", line)  # Never mistake markup for autolinks.
        for match in INLINE_LINK.finditer(scan):
            path, anchor = split_target(match.group(3))
            if not path and not anchor:
                continue
            resolve(path, anchor, lineno, "in '%s'" % match.group(0)[:60])
        for match in REFERENCE_USE.finditer(scan):
            label = match.group(3) or match.group(2)
            key = " ".join(label.split()).lower()
            if key not in definitions:
                # Ignore bare [brackets] with no matching definition.
                if match.group(3) == "" and match.group(0).endswith("[]"):
                    errors.append("%s:%d: undefined reference '[%s]'" % (relpath, lineno, label))
                elif match.group(3) != "":
                    errors.append("%s:%d: undefined reference '[%s]'" % (relpath, lineno, label))
                continue
            path, anchor = split_target(definitions[key])
            if not path and not anchor:
                continue
            resolve(path, anchor, lineno, "for reference '[%s]'" % label)
        for match in AUTOLINK.finditer(scan):
            raw = match.group(1)
            if SCHEME.match(raw):
                continue  # Remote or special schemes are never fetched.
            path, anchor = split_target(raw)
            if not path and not anchor:
                continue
            resolve(path, anchor, lineno, "in autolink")
    return errors


def main(argv):
    files_json = None
    repo = os.getcwd()
    index = 0
    while index < len(argv):
        arg = argv[index]
        if arg == "--files-json" and index + 1 < len(argv):
            files_json = argv[index + 1]
            index += 2
        elif arg == "--repo" and index + 1 < len(argv):
            repo = argv[index + 1]
            index += 2
        else:
            print("error: unknown argument: %s" % arg, file=sys.stderr)
            return 2
    if files_json is None:
        print("error: need --files-json", file=sys.stderr)
        return 2
    try:
        files = json.loads(files_json)
    except ValueError as exc:
        print("error: invalid --files-json: %s" % exc, file=sys.stderr)
        return 2
    if not isinstance(files, list):
        print("error: --files-json must be a list", file=sys.stderr)
        return 2
    repo = os.path.realpath(repo)
    cache = {}
    errors = []
    for relpath in sorted(set(files)):
        if not isinstance(relpath, str) or not relpath.endswith(".md"):
            continue
        full = os.path.normpath(os.path.join(repo, relpath))
        if full != repo and not full.startswith(repo + os.sep):
            errors.append("%s: path escapes repository" % relpath)
            continue
        if not os.path.isfile(full):
            continue  # Deleted or renamed-away files have no links to check.
        try:
            with open(full, "r", encoding="utf-8") as handle:
                cache[relpath] = document_anchors(handle.read().splitlines())
        except (OSError, UnicodeDecodeError) as exc:
            errors.append("%s: cannot read file: %s" % (relpath, exc))
            continue
        errors.extend(check_file(repo, relpath, cache))
    for error in errors:
        print(error)
    if errors:
        print("%d broken docs link(s) found" % len(errors), file=sys.stderr)
        return 1
    print("docs link check passed (%d file(s))" % len(cache))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
