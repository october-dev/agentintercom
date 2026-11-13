import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const classify = join(root, 'scripts', 'ci-docs-classify.py')
const check = join(root, 'scripts', 'ci-docs-check.py')

const classifyEntries = (entries) => {
  const out = execFileSync('python3', [classify, '--entries-json', JSON.stringify(entries)], {
    cwd: root,
    encoding: 'utf8'
  })
  const docsOnly = /^docs_only=(true|false)$/m.exec(out)?.[1] === 'true'
  const docsFiles = JSON.parse(/^docs_files=(.+)$/m.exec(out)?.[1] ?? 'null')
  return { docsOnly, docsFiles, out }
}

test('classifier fast-paths only the prose allowlist', () => {
  assert.equal(classifyEntries([['M', ['README.md']]]).docsOnly, true)
  const all = classifyEntries([
    ['M', ['README.md']],
    ['M', ['CONTRIBUTING.md']],
    ['M', ['CODE_OF_CONDUCT.md']],
    ['M', ['SECURITY.md']],
    ['M', ['CHANGELOG.md']],
    ['M', ['ROADMAP.md']],
    ['A', ['docs/releases.md']],
    ['A', ['docs/release-notes-9.9.9.md']]
  ])
  assert.equal(all.docsOnly, true)
  assert.deepEqual(all.docsFiles, [
    'CHANGELOG.md',
    'CODE_OF_CONDUCT.md',
    'CONTRIBUTING.md',
    'README.md',
    'ROADMAP.md',
    'SECURITY.md',
    'docs/release-notes-9.9.9.md',
    'docs/releases.md'
  ])
})

test('classifier fails closed on code, mixed, and sensitive paths', () => {
  const closed = [
    [['M', ['bus/store.go']]],
    [['M', ['README.md']], ['M', ['bus/store.go']]],
    [['M', ['.github/workflows/ci.yml']]],
    [['M', ['scripts/ci-docs-check.py']]],
    [['M', ['spec/README.md']]],
    [['M', ['examples/demo/main.go']]],
    [['M', ['go.mod']]],
    [['M', ['go.sum']]],
    [['M', ['sdk/typescript/package-lock.json']]],
    [['M', ['manifest.json']]],
    [['M', ['adapters/demo/adapter.json']]],
    [['M', ['docs/dependencies.md']]],
    [['M', ['docs/protocol-surface.md']]],
    [['M', ['docs/threat-model.md']]],
    [['M', ['docs/architecture/storage-backends.md']]],
    [['M', ['assets/agent-intercom-banner.png']]],
    [['M', ['README.md']], ['A', ['assets/new.png']]],
    [['M', ['docs/release-notes-9.9.9.txt']]],
    [['M', ['docs/nested/release-notes-1.md']]],
    [['M', ['../escape.md']]],
    [['M', ['/abs/path.md']]],
    [['M', ['docs\\windows.md']]],
    [],
    [[['M'], ['README.md']]],
    [['U', ['README.md']]],
    [['X', ['README.md']]],
    [['T', ['README.md']]],
    [['C123', ['README.md', 'CONTRIBUTING.md']]],
    [['M', ['README.md', 'CONTRIBUTING.md']]]
  ]
  for (const entries of closed) {
    assert.equal(classifyEntries(entries).docsOnly, false, JSON.stringify(entries))
  }
})

test('classifier handles renames and deletions conservatively', () => {
  assert.equal(classifyEntries([['D', ['README.md']]]).docsOnly, true)
  assert.equal(classifyEntries([['D', ['bus/store.go']]]).docsOnly, false)
  assert.equal(classifyEntries([['R100', ['README.md', 'CONTRIBUTING.md']]]).docsOnly, true)
  assert.equal(classifyEntries([['R100', ['README.md', 'docs/protocol-surface.md']]]).docsOnly, false)
  assert.equal(classifyEntries([['R100', ['bus/a.go', 'bus/b.go']]]).docsOnly, false)
  assert.equal(classifyEntries([['R100', ['README.md']]]).docsOnly, false)
})

test('classifier rejects malformed input loudly', () => {
  assert.throws(() => classifyEntries({}), /failed closed|non-zero|Command failed/)
  assert.throws(() => execFileSync('python3', [classify, '--bogus'], { cwd: root }), /Command failed/)
  assert.throws(() => execFileSync('python3', [classify], { cwd: root }), /Command failed/)
})

test('classifier resolves --base/--head diffs end to end in git', { timeout: 60_000 }, () => {
  const repo = mkdtempSync(join(tmpdir(), 'october-classify-test-'))
  try {
    const git = (args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' })
    git(['init', '-q'])
    git(['config', 'user.email', 'test@example.invalid'])
    git(['config', 'user.name', 'Classifier test'])
    git(['config', 'commit.gpgsign', 'false'])
    writeFileSync(join(repo, 'README.md'), '# Hi\n')
    writeFileSync(join(repo, 'main.go'), 'package main\n')
    git(['add', '.'])
    git(['commit', '-qm', 'base'])
    const base = git(['rev-parse', 'HEAD']).trim()
    writeFileSync(join(repo, 'README.md'), '# Hi there\n')
    git(['commit', '-qam', 'docs'])
    const docsOnly = git(['rev-parse', 'HEAD']).trim()
    writeFileSync(join(repo, 'main.go'), 'package main\n// change\n')
    git(['commit', '-qam', 'code'])
    const code = git(['rev-parse', 'HEAD']).trim()

    const run = (head, env = {}) =>
      execFileSync('python3', [classify, '--base', base, '--head', head, '--repo', repo], {
        cwd: root,
        encoding: 'utf8',
        env: { ...process.env, ...env }
      })
    assert.match(run(docsOnly), /^docs_only=true$/m)
    assert.match(run(docsOnly), /"README\.md"/)
    assert.match(run(code), /^docs_only=false$/m)
    // Unknown revisions fail closed to full checks without failing the job.
    assert.match(run('deadbeefdeadbeefdeadbeefdeadbeefdeadbeef'), /^docs_only=false$/m)

    const outputFile = join(repo, 'github-output')
    writeFileSync(outputFile, '')
    run(docsOnly, { GITHUB_OUTPUT: outputFile })
    const written = readFileSync(outputFile, 'utf8')
    assert.match(written, /^docs_only=true$/m)
    assert.match(written, /^docs_files=\[/m)
    assert.match(written, /^reason=.+$/m)
  } finally {
    rmSync(repo, { recursive: true, force: true })
  }
})

const checkFiles = (repo, files) => {
  try {
    const out = execFileSync('python3', [check, '--files-json', JSON.stringify(files), '--repo', repo], {
      encoding: 'utf8'
    })
    return { ok: true, out }
  } catch (error) {
    return { ok: false, out: `${error.stdout ?? ''}\n${error.stderr ?? ''}` }
  }
}

const checkAll = (repo) => {
  try {
    const out = execFileSync('python3', [check, '--all', '--repo', repo], {
      encoding: 'utf8'
    })
    return { ok: true, out }
  } catch (error) {
    return { ok: false, out: `${error.stdout ?? ''}\n${error.stderr ?? ''}` }
  }
}

test('docs checker validates links and anchors without network', { timeout: 60_000 }, () => {
  const repo = mkdtempSync(join(tmpdir(), 'october-docscheck-test-'))
  try {
    mkdirSync(join(repo, 'docs'), { recursive: true })
    writeFileSync(join(repo, 'docs', 'target.md'), '# Getting Started\n\nBody.\n\n# Getting Started\n\nDup.\n')
    writeFileSync(
      join(repo, 'README.md'),
      '# Title\n\nSee [target](docs/target.md) and [anchor](docs/target.md#getting-started) ' +
        'and [dup](docs/target.md#getting-started-1) and [self](#title).\n\n' +
        'Remote [web](https://example.invalid/x) and [mail](mailto:a@example.invalid) skipped.\n\n' +
        '```md\n[broken](nope.md)\n```\n\nInline `[code](#nope)` ignored.\n\n' +
        '[ref][guide] and [collapsed][] work.\n\n[guide]: docs/target.md\n[collapsed]: #title\n\n' +
        '<p align="center">\n<picture><br></picture>\n</p>\n\n' +
        'A `--flag <multi-line\nplaceholder>` code span is ignored.\n'
    )
    const good = checkFiles(repo, ['README.md'])
    assert.equal(good.ok, true, good.out)

    writeFileSync(join(repo, 'BAD.md'), '# Bad\n\n[missing](docs/nope.md) [anchor](#nope) ![img](img.png)\n')
    const bad = checkFiles(repo, ['BAD.md'])
    assert.equal(bad.ok, false)
    assert.match(bad.out, /BAD\.md:\d+: missing link target: docs\/nope\.md/)
    assert.match(bad.out, /BAD\.md:\d+: unknown anchor '#nope'/)
    assert.match(bad.out, /BAD\.md:\d+: missing link target: img\.png/)

    writeFileSync(join(repo, 'REF.md'), '# Ref\n\n[undef][nope] and [shortcut].\n\n[shortcut]: #ref\n')
    const ref = checkFiles(repo, ['REF.md'])
    assert.equal(ref.ok, false, ref.out)
    assert.match(ref.out, /undefined reference '\[nope\]'/)

    // Deleted files, non-markdown input, and empty sets pass trivially.
    assert.equal(checkFiles(repo, ['GONE.md']).ok, true)
    assert.equal(checkFiles(repo, ['main.go']).ok, true)
    assert.equal(checkFiles(repo, []).ok, true)
    // Escapes and unreadable targets fail rather than pass silently.
    assert.equal(checkFiles(repo, ['../escape.md']).ok, false)
  } finally {
    rmSync(repo, { recursive: true, force: true })
  }
})

test('docs checker follows HTML links and own-repo blob URLs', { timeout: 60_000 }, () => {
  const repo = mkdtempSync(join(tmpdir(), 'october-docshtml-test-'))
  try {
    mkdirSync(join(repo, 'docs'), { recursive: true })
    mkdirSync(join(repo, 'assets'), { recursive: true })
    writeFileSync(join(repo, 'docs', 'target.md'), '# Getting Started\n')
    writeFileSync(join(repo, 'assets', 'logo.svg'), '<svg></svg>\n')
    const blob = 'https://github.com/october-dev/agentintercom/blob/main/docs/target.md'
    writeFileSync(
      join(repo, 'README.md'),
      '# Title\n\n' +
        '<a href="#title">nav</a> <a href="docs/target.md">rel</a>\n\n' +
        '<img src="assets/logo.svg" alt="logo">\n\n' +
        '<source srcset="assets/logo.svg 1x, assets/logo.svg 2x">\n\n' +
        `[page](${blob}) and [frag](${blob}#getting-started).\n\n` +
        'Pinned [old](https://github.com/october-dev/october-bus/blob/7fc501ac58a1e1cda8195a70ba02f2e9a9ba8ad1/spec/0.1/mcp.md#L44) skipped.\n\n' +
        'Foreign [x](https://github.com/other/repo/blob/main/x.md) skipped.\n'
    )
    const good = checkFiles(repo, ['README.md'])
    assert.equal(good.ok, true, good.out)

    writeFileSync(
      join(repo, 'BROKEN.md'),
      '# Broken\n\n' +
        '<a href="#missing">nav</a>\n\n' +
        '<img src="assets/gone.svg">\n\n' +
        '<source srcset="assets/logo.svg 1x, assets/gone2.svg 2x">\n\n' +
        '[gone](https://github.com/october-dev/agentintercom/blob/main/docs/gone.md)\n\n' +
        `[badfrag](${blob}#no-such-anchor)\n`
    )
    const bad = checkFiles(repo, ['BROKEN.md'])
    assert.equal(bad.ok, false, bad.out)
    assert.match(bad.out, /BROKEN\.md:\d+: unknown anchor '#missing'/)
    assert.match(bad.out, /BROKEN\.md:\d+: missing link target: assets\/gone\.svg/)
    assert.match(bad.out, /BROKEN\.md:\d+: missing link target: assets\/gone2\.svg/)
    assert.match(bad.out, /BROKEN\.md:\d+: missing link target: docs\/gone\.md/)
    assert.match(bad.out, /BROKEN\.md:\d+: unknown anchor '#no-such-anchor' in docs\/target\.md/)
  } finally {
    rmSync(repo, { recursive: true, force: true })
  }
})

test('docs checker ignores autolink-like text and slugs headings like GitHub', { timeout: 60_000 }, () => {
  const repo = mkdtempSync(join(tmpdir(), 'october-docsslug-test-'))
  try {
    writeFileSync(
      join(repo, 'NOTE.md'),
      '# A & B\n\n' +
        'Contact <conduct@example.com> or edit <workspace-id> and <agent_id>.\n\n' +
        'Ampersand [ok](#a--b) works.\n\n' +
        '```md\n# fenced-heading\n```\n'
    )
    const good = checkFiles(repo, ['NOTE.md'])
    assert.equal(good.ok, true, good.out)

    writeFileSync(
      join(repo, 'SLUG-BAD.md'),
      '# A & B\n\n' +
        'Single [hyphen](#a-b) does not exist on GitHub.\n\n' +
        '```md\n# fenced-heading\n```\n\n' +
        'Fenced [ghost](#fenced-heading) is not a heading.\n'
    )
    const bad = checkFiles(repo, ['SLUG-BAD.md'])
    assert.equal(bad.ok, false, bad.out)
    assert.match(bad.out, /SLUG-BAD\.md:\d+: unknown anchor '#a-b'/)
    assert.match(bad.out, /SLUG-BAD\.md:\d+: unknown anchor '#fenced-heading'/)
  } finally {
    rmSync(repo, { recursive: true, force: true })
  }
})

test('docs checker --all validates every tracked Markdown file', { timeout: 60_000 }, () => {
  const repo = mkdtempSync(join(tmpdir(), 'october-docsall-test-'))
  try {
    const git = (args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' })
    git(['init', '-q'])
    git(['config', 'user.email', 'test@example.invalid'])
    git(['config', 'user.name', 'Docs check test'])
    git(['config', 'commit.gpgsign', 'false'])
    writeFileSync(join(repo, 'README.md'), '# Hi\n\n[ok](#hi)\n')
    mkdirSync(join(repo, 'docs'), { recursive: true })
    writeFileSync(join(repo, 'docs', 'other.md'), '# Other\n\n[back](../README.md#hi)\n')
    git(['add', '.'])
    git(['commit', '-qm', 'base'])
    const good = checkAll(repo)
    assert.equal(good.ok, true, good.out)
    assert.match(good.out, /docs link check passed \(2 file\(s\)\)/)

    // A broken inbound link in any tracked file fails the whole check ...
    writeFileSync(join(repo, 'docs', 'other.md'), '# Other\n\n[back](../README.md#renamed-away)\n')
    git(['commit', '-qam', 'break'])
    const bad = checkAll(repo)
    assert.equal(bad.ok, false, bad.out)
    assert.match(bad.out, /other\.md:\d+: unknown anchor '#renamed-away' in \.\.\/README\.md/)

    // ... while untracked scratch files are invisible to --all.
    writeFileSync(join(repo, 'SCRATCH.md'), '# Scratch\n\n[nope](#nope)\n')
    const bad2 = checkAll(repo)
    assert.equal(bad2.ok, false, bad2.out)
    assert.doesNotMatch(bad2.out, /SCRATCH/)

    // Conflicting inputs are a usage error, not a silent default.
    assert.throws(() => execFileSync('python3', [check, '--all', '--files-json', '[]'], { cwd: root }), /Command failed/)
  } finally {
    rmSync(repo, { recursive: true, force: true })
  }
})
