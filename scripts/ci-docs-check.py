#!/usr/bin/env python3
"""Local Markdown link and anchor checker for the CI docs fast path (#188).

Validates outgoing links from Markdown files without any network access:
relative file targets must exist inside the repository, ``#anchor`` targets
must match a GitHub-style heading slug in the target document, and reference
definitions must resolve. HTML ``href``/``src``/``srcset`` attributes and
links to our own repository's ``blob``/``tree``/``raw`` pages are checked the
same way. Anything else with a URI scheme is skipped (never fetched).

Kept dependency-free on purpose: the check must run on a pristine runner
with only CPython and git, stay fully offline, and approximate GitHub's
heading slugs closely, without a version-pinned third-party install.
Covered by ``scripts/ci-docs-fastpath.test.mjs``.

Deliberate policy (reported, not skipped): links that escape the
repository (``../../issues``), anchors with no matching heading
(including ``#top``), and ``[text][label]`` uses with no matching
definition. Fenced blocks, indented code, HTML comments, and code spans
are never link-checked; uncertain cases stay lenient (hidden) rather
than failing valid documents.

Usage::

    python3 scripts/ci-docs-check.py --all [--repo DIR]
    python3 scripts/ci-docs-check.py --files-json '["README.md"]' [--repo DIR]

Exits 0 when every check passes (or there is nothing to check) and 1 with
``file:line: message`` diagnostics otherwise.
"""

import json
import os
import re
import subprocess
import sys
from urllib.parse import unquote

INLINE_LINK = re.compile(r"(!?)\[([^\]]*)\]\(([^)]*)\)")
REFERENCE_USE = re.compile(r"(!?)\[([^\]]+)\]\[([^\]]*)\]")
REFERENCE_DEF = re.compile(r"^[ ]{0,3}\[([^\]]+)\]:[ \t]*(\S+)[ \t]*(\"([^\"]*)\"|'([^']*)'|\(([^)]*)\))?[ \t]*$")
HEADING = re.compile(r"^(#{1,6})\s+(.+?)[ \t]*#*[ \t]*$")
SETEXT = re.compile(r"^(=+|-+)[ \t]*$")
FENCED = re.compile(r"^(```+|~~~+)")
ESCAPED_PUNCT = re.compile(r"\\([!\"#$%&'()*+,\-./:;<=>?@\[\\\]^_`{|}~])")
SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")
HTML_ANCHOR = re.compile(r'<a\s+[^>]*?(?:id|name)\s*=\s*["\']([^"\']+)["\']', re.IGNORECASE)
HTML_TAG = re.compile(r"</?[A-Za-z][A-Za-z0-9]*(?:\s+[^<>]*)?/?>")
HTML_LINK_ATTR = re.compile(
    r"(?<![A-Za-z0-9_-])(?:href|src)\s*=\s*(?:\"([^\"]*)\"|'([^']*)'|([^\s>]+))",
    re.IGNORECASE,
)
HTML_SRCSET_ATTR = re.compile(
    r"(?<![A-Za-z0-9_-])srcset\s*=\s*(?:\"([^\"]*)\"|'([^']*)'|([^\s>]+))",
    re.IGNORECASE,
)
# Only default-branch pages are rewritten: pinned commits and feature
# branches cannot be validated against the checkout, so those stay remote
# (skipped, never fetched). october-bus is the repository's legacy name.
OWN_REPO_PAGE = re.compile(
    r"^https?://github\.com/october-dev/(?:agentintercom|october-bus)"
    r"/(?:blob|tree)/(?:main|master|HEAD)/(.+?)/?$"
)
OWN_REPO_RAW = re.compile(
    r"^https?://raw\.githubusercontent\.com/october-dev/(?:agentintercom|october-bus)"
    r"/(?:main|master|HEAD)/(.+?)/?$"
)
LINE_ANCHOR = re.compile(r"^L\d+")


def github_slug(heading):
    """GitHub-style heading slug from the heading's rendered text.

    Inline formatting is removed the way GitHub's renderer consumes it
    before the slug rules (lowercase, punctuation stripped, each space a
    hyphen): code spans keep their content, links keep their text, images
    and HTML tags contribute nothing, and emphasis delimiters are dropped
    while intra-word underscores survive. An approximation for exotic
    nesting; covered by regression tests for the common forms.
    """
    text = re.sub(r"`+([^`]*)`+", r"\1", heading)  # code spans
    text = re.sub(r"!\[([^\]]*)\]\([^)]*\)", "", text)  # images: no text
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)  # inline links
    text = re.sub(r"\[([^\]]+)\]\[[^\]]*\]", r"\1", text)  # reference links
    text = re.sub(r"<([^<>@]*@[^<>]*)>", r"\1", text)  # email autolinks
    text = re.sub(r"</?[A-Za-z][^<>]*>", "", text)  # remaining HTML tags
    text = re.sub(r"(?<!\w)_|_(?!\w)", "", text)  # emphasis underscores
    text = text.strip().lower()
    text = re.sub(r"[^\w \-]", "", text, flags=re.UNICODE)
    return text.replace(" ", "-")


def rewrite_own_repo_url(path):
    """Map our own repo's blob/tree/raw page URLs to root-anchored paths.

    GitHub blob URLs are relative to the repository root no matter which
    document links to them, so the rewrite keeps a leading ``/`` and the
    resolver below treats it as root-anchored.
    """
    bare = path.split("?", 1)[0]
    for pattern in (OWN_REPO_PAGE, OWN_REPO_RAW):
        match = pattern.match(bare)
        if match:
            return "/" + match.group(1)
    return path


def srcset_candidates(value):
    """Split a srcset attribute into candidate URLs (descriptors dropped)."""
    if value.strip().lower().startswith("data:"):
        return []
    candidates = []
    for chunk in value.split(","):
        token = chunk.strip().split()
        if token:
            candidates.append(token[0])
    return candidates


def document_anchors(lines):
    """Collect anchor slugs defined by headings and explicit HTML anchors.

    Both ATX (``#``) and setext (``===``/``---`` underline) headings count.
    Duplicate slugs get ``-1``, ``-2`` ... suffixes like github-slugger:
    the suffix scan skips every slug already taken, so ``Setup``,
    ``Setup``, ``Setup-1`` yield ``setup``, ``setup-1``, ``setup-1-1``.
    """
    anchors = set()
    counts = {}

    def add(base):
        seen = counts.get(base, 0)
        candidate = base if seen == 0 else "%s-%d" % (base, seen)
        while candidate in anchors:
            seen += 1
            candidate = "%s-%d" % (base, seen)
        counts[base] = seen + 1
        anchors.add(candidate)

    prev = None  # stripped previous line, for setext underlines
    for line in lines:
        for anchor in HTML_ANCHOR.findall(line):
            anchors.add(anchor)
        match = HEADING.match(line)
        if match:
            add(github_slug(match.group(2)))
            prev = None
            continue
        if SETEXT.match(line):
            if prev is not None:
                add(github_slug(prev))
            prev = None
            continue
        stripped = line.strip()
        if (
            not stripped
            or stripped.startswith(("#", ">", "-", "*", "+", "`", "~"))
            or re.match(r"\d+[.)]\s", stripped)
            or REFERENCE_DEF.match(line)
        ):
            prev = None  # blank, block markup, list item, or definition
        else:
            prev = stripped
    return anchors


def blank_spans(text):
    """Blank CommonMark-style code spans, which may cross line breaks.

    A run of N backticks is closed by the next run of exactly N backticks;
    longer or shorter runs are span content. An unmatched run is literal
    text. Wrongly pairing two literal backticks only hides text from
    validation (lenient), never flags valid docs.
    """
    out = list(text)
    total = len(text)
    index = 0
    while index < total:
        if text[index] != "`":
            index += 1
            continue
        end = index
        while end < total and text[end] == "`":
            end += 1
        run_len = end - index
        pos = end
        closed = -1
        while pos < total:
            found = text.find("`", pos)
            if found == -1:
                break
            found_end = found
            while found_end < total and text[found_end] == "`":
                found_end += 1
            if found_end - found == run_len:
                closed = found
                break
            pos = found_end
        if closed == -1:
            index = end
            continue
        for blank_pos in range(index, closed + run_len):
            if out[blank_pos] != "\n":
                out[blank_pos] = " "
        index = closed + run_len
    return "".join(out)


def blank_comments(text):
    """Blank HTML comments, which may span lines; unclosed runs to the end."""
    out = list(text)
    index = 0
    while True:
        start = text.find("<!--", index)
        if start == -1:
            break
        end = text.find("-->", start)
        end = len(text) if end == -1 else end + 3
        for pos in range(start, end):
            if out[pos] != "\n":
                out[pos] = " "
        index = end
    return "".join(out)


def strip_fences(lines):
    """Blank fenced and indented code blocks; code spans are left intact.

    A fence closes only on the same marker character with at least as many
    backticks/tildes and nothing else on the line, so a longer fence can
    hold a shorter example. Four-space-indented lines are code when they
    start a block (previous line blank) or continue one; indented lines
    directly under list or paragraph content stay visible so their links
    are still checked.
    """
    out = []
    fence = None  # (marker char, opening run length)
    prev_blank = True
    indent_code = False
    for line in lines:
        stripped = line.lstrip()
        if not stripped:
            out.append("" if fence else line)
            prev_blank = True
            continue
        indented = line.startswith("    ") or line.startswith("\t")
        if fence is None and indented and (prev_blank or indent_code):
            out.append("")
            indent_code = True
            prev_blank = False
            continue
        indent_code = False
        match = FENCED.match(stripped)
        if match and fence is None:
            info = stripped[match.end():]
            if match.group(1)[0] == "`" and "`" in info:
                out.append(line)  # info string with backtick: not a fence
            else:
                fence = (match.group(1)[0], len(match.group(1)))
                out.append("")
        elif (
            match
            and fence is not None
            and match.group(1)[0] == fence[0]
            and len(match.group(1)) >= fence[1]
            and stripped[match.end():].strip() == ""
        ):
            fence = None
            out.append("")
            prev_blank = True  # a fence boundary starts a fresh block
            continue
        else:
            out.append("" if fence else line)
        prev_blank = False
    return out


def strip_code(lines):
    """Blank code blocks, HTML comments, and code spans: never link-checked."""
    return blank_spans(blank_comments("\n".join(strip_fences(lines)))).split("\n")


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
        path = rewrite_own_repo_url(path)
        if anchor and LINE_ANCHOR.match(anchor):
            anchor = ""  # Pinned line links (#L10) have no heading slug.
        if SCHEME.match(path) or path.startswith("//"):
            return  # Remote, protocol-relative, or special URLs: never fetched.
        # Relative links may carry a query (?raw=1) or percent-escapes
        # (my%20file.md); both are resolved the way GitHub serves them.
        path = unquote(path.split("?", 1)[0])
        if not path:
            if anchor and anchor not in cache[relpath]:
                errors.append("%s:%d: unknown anchor '#%s' %s" % (relpath, lineno, anchor, note))
            return
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
                        cache[target_rel] = document_anchors(
                            strip_fences(handle.read().splitlines())
                        )
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
        for tag in HTML_TAG.findall(line):
            for match in HTML_LINK_ATTR.finditer(tag):
                value = match.group(1) or match.group(2) or match.group(3) or ""
                path, anchor = split_target(value)
                if not path and not anchor:
                    continue
                resolve(path, anchor, lineno, "in '%s'" % tag[:60])
            for match in HTML_SRCSET_ATTR.finditer(tag):
                value = match.group(1) or match.group(2) or match.group(3) or ""
                for candidate in srcset_candidates(value):
                    path, anchor = split_target(candidate)
                    if not path and not anchor:
                        continue
                    resolve(path, anchor, lineno, "in srcset '%s'" % tag[:60])
        # Never mistake markup for links; backslash-escaped punctuation is
        # literal text, so `\[x\](y)` is not a link.
        scan = ESCAPED_PUNCT.sub("  ", HTML_TAG.sub("", line))
        for match in INLINE_LINK.finditer(scan):
            path, anchor = split_target(match.group(3))
            if not path and not anchor:
                continue
            resolve(path, anchor, lineno, "in '%s'" % match.group(0)[:60])
        for match in REFERENCE_USE.finditer(scan):
            label = match.group(3) or match.group(2)
            key = " ".join(label.split()).lower()
            if key not in definitions:
                # Single [brackets] never match this pattern, so anything here
                # is a real [text][label] or [label][] reference: undefined.
                errors.append("%s:%d: undefined reference '[%s]'" % (relpath, lineno, label))
                continue
            path, anchor = split_target(definitions[key])
            if not path and not anchor:
                continue
            resolve(path, anchor, lineno, "for reference '[%s]'" % label)
        # No autolink branch: CommonMark only autolinks absolute URIs (skipped
        # above when they carry a scheme) and email addresses, so bare
        # <placeholders> and <name@example.com> are plain text, not files.
    return errors


def tracked_markdown(repo):
    """List tracked Markdown files via git, failing closed when unavailable."""
    proc = subprocess.run(
        ["git", "ls-files", "-z", "*.md"],
        cwd=repo,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(
            "git ls-files failed: %s" % proc.stderr.decode("utf-8", "replace").strip()
        )
    try:
        return [token.decode("utf-8", "strict") for token in proc.stdout.split(b"\0") if token]
    except UnicodeDecodeError as exc:
        raise RuntimeError("undecodable tracked filename: %s" % exc)


def main(argv):
    files_json = None
    all_tracked = False
    repo = os.getcwd()
    index = 0
    while index < len(argv):
        arg = argv[index]
        if arg == "--files-json" and index + 1 < len(argv):
            files_json = argv[index + 1]
            index += 2
        elif arg == "--all":
            all_tracked = True
            index += 1
        elif arg == "--repo" and index + 1 < len(argv):
            repo = argv[index + 1]
            index += 2
        else:
            print("error: unknown argument: %s" % arg, file=sys.stderr)
            return 2
    if all_tracked and files_json is not None:
        print("error: --all and --files-json are mutually exclusive", file=sys.stderr)
        return 2
    if not all_tracked and files_json is None:
        print("error: need --all or --files-json", file=sys.stderr)
        return 2
    repo = os.path.realpath(repo)
    if all_tracked:
        try:
            files = tracked_markdown(repo)
        except RuntimeError as exc:
            print("error: %s" % exc, file=sys.stderr)
            return 1
    else:
        try:
            files = json.loads(files_json)
        except ValueError as exc:
            print("error: invalid --files-json: %s" % exc, file=sys.stderr)
            return 2
        if not isinstance(files, list):
            print("error: --files-json must be a list", file=sys.stderr)
            return 2
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
                cache[relpath] = document_anchors(strip_fences(handle.read().splitlines()))
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
