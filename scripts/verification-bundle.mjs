import { createHash } from 'node:crypto'
import { closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, readSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { stripVTControlCharacters } from 'node:util'
import { fileURLToPath } from 'node:url'

const maxLogBytes = 16 * 1024 * 1024
const defaultSecretNames = ['OCTOBER_BUS_ADMIN_TOKEN', 'OCTOBER_BUS_SCOPE_TOKEN', 'OCTOBER_BUS_AGENT_TOKEN', 'GH_TOKEN', 'GITHUB_TOKEN', 'NPM_TOKEN', 'NODE_AUTH_TOKEN', 'OPENAI_API_KEY', 'ANTHROPIC_API_KEY']
const fields = ['harnessFamily', 'harnessVersion', 'adapterId', 'adapterVersion', 'protocolVersion', 'runtimeVersion', 'operatingSystem', 'architecture', 'profile', 'repositoryCommit', 'verificationMode', 'attemptedAt', 'outcome', 'limitations']
const optionalFields = ['launchMode', 'model']
const check = (condition, message) => { if (!condition) throw new Error(message) }
const digest = bytes => `sha256:${createHash('sha256').update(bytes).digest('hex')}`

function readLimited(path, limit) {
  check(lstatSync(path).isFile(), 'Inputs must be regular files, not directories or symbolic links')
  const fd = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0))
  try {
    check(fstatSync(fd).isFile(), 'Inputs must be regular files')
    check(fstatSync(fd).size <= limit, 'Input exceeds the size limit')
    const buffer = Buffer.alloc(limit + 1)
    let size = 0
    while (size < buffer.length) {
      const count = readSync(fd, buffer, size, buffer.length - size, null)
      if (count === 0) break
      size += count
    }
    check(size <= limit, 'Input exceeds the size limit')
    return buffer.subarray(0, size)
  } finally { closeSync(fd) }
}

function readJSON(path) {
  const bytes = readLimited(path, 64 * 1024)
  try { return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)) }
  catch { throw new Error('Input must contain valid UTF-8 JSON') }
}

export function validateAttempt(metadata) {
  check(metadata !== null && typeof metadata === 'object' && !Array.isArray(metadata), 'Attempt metadata must be an object')
  check(Object.keys(metadata).every(field => fields.includes(field) || optionalFields.includes(field)) && fields.every(field => Object.hasOwn(metadata, field)), 'Attempt metadata has missing or unknown fields')
  for (const field of [...fields.filter(field => field !== 'limitations'), ...optionalFields.filter(field => Object.hasOwn(metadata, field))]) {
    check(typeof metadata[field] === 'string' && metadata[field].trim().length > 0 && metadata[field].length <= 256 && !/[\x00-\x1f\x7f]/.test(metadata[field]), `Invalid metadata field: ${field}`)
  }
  check(/^[a-z0-9][a-z0-9-]{0,63}$/.test(metadata.adapterId), 'Invalid adapterId')
  check(/^\d+\.\d+$/.test(metadata.protocolVersion), 'Invalid protocolVersion')
  check(/^[a-f0-9]{40}$/.test(metadata.repositoryCommit), 'Use a full immutable repository commit')
  for (const [field, values] of Object.entries({ operatingSystem: ['macos', 'linux', 'windows'], architecture: ['amd64', 'arm64'], profile: ['mcp-adapter', 'native-adapter'], verificationMode: ['automated', 'assisted', 'manual'], outcome: ['passed', 'failed', 'partial', 'not-run'] })) {
    check(values.includes(metadata[field]), `Invalid metadata field: ${field}`)
  }
  const instant = new Date(metadata.attemptedAt)
  check(Number.isFinite(instant.getTime()) && instant.toISOString().replace('.000Z', 'Z') === metadata.attemptedAt.replace('.000Z', 'Z'), 'attemptedAt must be an ISO UTC timestamp')
  check(Array.isArray(metadata.limitations) && metadata.limitations.length <= 50 && metadata.limitations.every(value => typeof value === 'string' && value.trim().length > 0 && value.length <= 1024), 'Invalid limitations')
  return metadata
}

export function redactLog(input, { secrets = [], homeDirectory } = {}) {
  let text = stripVTControlCharacters(input).replace(/\r\n?/g, '\n').replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '')
  const rules = new Set(text === input ? [] : ['terminal-controls'])
  const replace = (pattern, replacement, rule) => {
    const next = text.replace(pattern, replacement)
    if (next !== text) rules.add(rule)
    text = next
  }
  const literal = (value, replacement, rule) => {
    if (value && text.includes(value)) { text = text.split(value).join(replacement); rules.add(rule) }
  }
  const variants = secrets.flatMap(value => [value, encodeURIComponent(value), JSON.stringify(value).slice(1, -1)])
  for (const value of [...new Set(variants)].sort((a, b) => b.length - a.length)) literal(value, '[REDACTED]', 'explicit-secrets')
  if (homeDirectory && homeDirectory.length > 1) {
    for (const value of [homeDirectory, JSON.stringify(homeDirectory).slice(1, -1), encodeURIComponent(homeDirectory)]) literal(value, '[HOME]', 'home-directory')
  }
  replace(/\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+/gi, '$1 [REDACTED]', 'authorization')
  const key = '(?:[A-Za-z_][A-Za-z0-9_]*(?:token|secret|password|api_?key)|token|secret|password|api_?key|credential)'
  replace(new RegExp(`("${key}"\\s*:\\s*)"(?:\\\\.|[^"\\\\])*"`, 'gi'), '$1"[REDACTED]"', 'credential-fields')
  replace(new RegExp(`(\\b${key}\\s*=\\s*)(?:"(?:\\\\.|[^"\\\\])*"|'[^']*'|[^\\s,;&]+)`, 'gi'), '$1[REDACTED]', 'credential-fields')
  replace(/(https?:\/\/)[^\s/@]+:[^\s/@]+@/gi, '$1[REDACTED]@', 'url-credentials')
  return { text, rules: [...rules].sort() }
}

export function createVerificationBundle({ metadataPath, logPath, outDir, redactEnv = [], env = process.env }) {
  for (const name of redactEnv) {
    check(/^[A-Za-z_][A-Za-z0-9_]*$/.test(name) && typeof env[name] === 'string' && env[name].length > 0, 'Each --redact-env must name a nonempty environment variable')
  }
  const secrets = [...new Set([...defaultSecretNames, ...redactEnv])].map(name => env[name]).filter(value => typeof value === 'string' && value.length > 0)
  const redaction = { secrets, homeDirectory: env.HOME ?? env.USERPROFILE }
  const metadata = validateAttempt(readJSON(metadataPath))
  const metadataJSON = JSON.stringify(metadata)
  check(redactLog(metadataJSON, redaction).text === metadataJSON, 'Metadata contains sensitive values; sanitize it before bundling')
  let rawLog
  try { rawLog = new TextDecoder('utf-8', { fatal: true }).decode(readLimited(logPath, maxLogBytes)) }
  catch (error) {
    if (error instanceof TypeError) throw new Error('Log must contain valid UTF-8 text')
    throw error
  }
  check(rawLog.trim().length > 0, 'Log must not be empty; include setup observations for a not-run attempt')
  const sanitized = redactLog(rawLog, redaction)
  check(sanitized.text.trim().length > 0, 'Sanitized log must not be empty')
  check(Buffer.byteLength(sanitized.text) <= maxLogBytes, 'Sanitized log exceeds the size limit')
  const manifest = {
    schemaVersion: 1, kind: 'agentintercom-verification-bundle', verificationStatus: 'unreviewed',
    createdAt: new Date().toISOString(), metadata,
    log: { path: 'run.log', resultDigest: digest(sanitized.text), bytes: Buffer.byteLength(sanitized.text) },
    redaction: { rules: sanitized.rules, manualReviewRequired: true }
  }
  // Never copy the raw log, overwrite an existing bundle, execute a harness,
  // upload artifacts, or change the verified registry.
  mkdirSync(outDir, { mode: 0o700 })
  const write = (name, text) => writeFileSync(join(outDir, name), text, { encoding: 'utf8', mode: 0o600, flag: 'wx' })
  write('run.log', sanitized.text)
  write('bundle.json', `${JSON.stringify(manifest, null, 2)}\n`)
  write('REVIEW.md', '# Unreviewed verification bundle\n\nRedaction is best effort, not a privacy guarantee. Inspect run.log and bundle.json for credentials, private prompts, personal paths, URLs, and project data before sharing.\n\nThis bundle does not certify a harness. Check the exact versions, immutable commit, all required runbook scenarios, approvals, failures, and limitations. Partial and not-run attempts are observations, not formal passing evidence.\n\nIf the log needs more redaction, regenerate the bundle from a corrected private input or with additional --redact-env names. Verify its digest after any change. Publish only after explicit human review; never upload the raw source log. Independent review is required before creating a formal evidence record or changing the verified registry.\n')
  return manifest
}

export function verifyVerificationBundle(directory) {
  check(lstatSync(directory).isDirectory(), 'Bundle must be a real directory, not a symbolic link')
  const bundle = readJSON(join(directory, 'bundle.json'))
  check(bundle?.schemaVersion === 1 && bundle.kind === 'agentintercom-verification-bundle' && bundle.verificationStatus === 'unreviewed' && bundle.redaction?.manualReviewRequired === true, 'Invalid unreviewed bundle manifest')
  validateAttempt(bundle.metadata)
  check(bundle.log?.path === 'run.log', 'Invalid bundle log path')
  const log = readLimited(join(directory, 'run.log'), maxLogBytes)
  check(bundle.log.bytes === log.length && bundle.log.resultDigest === digest(log), 'Bundle log digest or size mismatch; regenerate the bundle')
  return bundle
}

function main(args) {
  if (args.length === 1 && args[0] === '--help') {
    console.log('Create: node scripts/verification-bundle.mjs --metadata FILE --log FILE --out NEW_DIRECTORY [--redact-env NAME ...]\nCheck:  node scripts/verification-bundle.mjs verify DIRECTORY\nLocal unreviewed artifacts only. Redaction requires manual review before sharing.')
    return
  }
  if (args.length === 2 && args[0] === 'verify') {
    verifyVerificationBundle(args[1])
    console.log('Bundle log digest valid. This is not a signature or harness certification; status remains unreviewed.')
    return
  }
  const options = { redactEnv: [] }
  const flags = { '--metadata': 'metadataPath', '--log': 'logPath', '--out': 'outDir' }
  for (let index = 0; index < args.length; index += 2) {
    const flag = args[index], value = args[index + 1]
    check(typeof value === 'string' && !value.startsWith('--'), 'Expected a value after each option; see --help')
    if (flag === '--redact-env') options.redactEnv.push(value)
    else {
      check(Object.hasOwn(flags, flag) && !Object.hasOwn(options, flags[flag]), 'Unknown or repeated option; see --help')
      options[flags[flag]] = value
    }
  }
  check(Object.values(flags).every(name => options[name]), 'Expected --metadata, --log, and --out; see --help')
  createVerificationBundle(options)
  console.log('Created a local, unreviewed bundle. Inspect it for sensitive data before sharing. Nothing was uploaded or verified.')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main(process.argv.slice(2)) }
  catch (error) {
    // Native filesystem errors can contain private paths; JSON parser errors
    // can quote input. Neither belongs in a shareable CLI diagnostic.
    console.error(error.code ? `Verification bundle failed (${error.code}); check input files and the new output directory.` : error.message)
    process.exitCode = 1
  }
}
