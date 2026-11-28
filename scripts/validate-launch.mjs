import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { artifacts, platformPackage, root, sdk } from './npm-distribution.mjs'

export function validationPlan({ allPlatforms = false, upgrade = false, scriptsOnly = false } = {}) {
  const node = (label, args, extra = {}) => ({ label, command: process.execPath, args, cwd: root, ...extra })
  const npm = (label, args, extra = {}) => node(label, [process.env.npm_execpath ?? '<npm-cli>', 'run', ...args], { cwd: sdk, ...extra })
  const scripts = readdirSync(join(root, 'scripts')).filter(name => name.endsWith('.test.mjs')).sort().map(name => join(root, 'scripts', name))
  const metadata = [node('Release and tooling regressions', ['--test', ...scripts]), node('Compatibility metadata', ['scripts/check-compatibility.mjs'])]
  if (scriptsOnly) return metadata
  const native = platformPackage()
  const env = { ...process.env, OCTOBER_BUS_BINARY: join(artifacts, native.target, 'bin', native.binary) }
  const selection = allPlatforms ? ['--', '--all'] : []
  return [
    { label: 'Patch whitespace', command: 'git', args: ['diff', 'HEAD', '--check'], cwd: root },
    ...metadata,
    { label: 'Go race and regression suite', command: 'go', args: ['test', '-race', '-count=1', './...'], cwd: root },
    { label: 'Go static analysis', command: 'go', args: ['vet', './...'], cwd: root },
    npm('SDK typecheck', ['typecheck']), npm('SDK build', ['build']), npm('SDK errors and lifecycle', ['test:errors']),
    npm('Native binaries', ['build:native', ...selection]), npm('Distribution packing', ['pack:distribution', ...selection]),
    npm('SDK / Go integration', ['test:integration'], { env }), npm('Fresh npm installation', ['test:distribution']),
    ...(upgrade ? [node('Released-binary upgrade / rollback', ['scripts/rehearse-upgrade.mjs'], { env })] : [])
  ]
}

export function runValidation(plan, execute = execFileSync) {
  for (const [index, step] of plan.entries()) {
    console.log(`[${index + 1}/${plan.length}] ${step.label}`)
    try {
      execute(step.command, step.args, { cwd: step.cwd, env: step.env ?? process.env, stdio: 'inherit', timeout: 15 * 60_000 })
    } catch {
      console.error(`FAILED: ${step.label}. ${plan.length - index - 1} later stages were not run.`)
      return 1
    }
  }
  console.log('Selected checks passed. This does not certify live harnesses, signing, a soak test, or a public npm release.')
  return 0
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2)
  assert.ok(args.every(arg => ['--dry-run', '--all-platforms', '--upgrade', '--scripts-only'].includes(arg)), 'Expected [--dry-run] [--all-platforms] [--upgrade] [--scripts-only]')
  const plan = validationPlan({ allPlatforms: args.includes('--all-platforms'), upgrade: args.includes('--upgrade'), scriptsOnly: args.includes('--scripts-only') })
  if (args.includes('--dry-run')) {
    for (const step of plan) console.log(`${step.label}: ${JSON.stringify([step.command, ...step.args])}`)
    console.log('Dry run only; no checks executed, no dependencies installed.')
  } else {
    assert.ok(process.env.npm_execpath, 'Use npm run validate:launch from sdk/typescript')
    if (args.includes('--upgrade')) assert.ok(process.env.OCTOBER_BUS_PREVIOUS_BINARY, 'Supply an already downloaded, checksum-verified previous-release binary using OCTOBER_BUS_PREVIOUS_BINARY')
    process.exitCode = runValidation(plan)
  }
}
