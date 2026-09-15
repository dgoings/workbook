'use strict'

// Locating and driving the `workbook` binary.
//
// Workbench never links Workbook's Go packages: they all live under internal/,
// which Go forbids other modules from importing. The binary's --json envelope
// and the HTTP API its `serve` exposes are the whole integration surface.

const { spawn } = require('node:child_process')
const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')

// Windows executables carry an extension and PATH lookup does not add one, so
// every place that names the binary has to agree on this.
const BINARY = process.platform === 'win32' ? 'workbook.exe' : 'workbook'

// Where an installed binary tends to land, searched only when the app has no
// bundled copy of its own — which in practice means a development run.
//
// A packaged app does not inherit a shell PATH: launched from Finder, macOS
// gives it /usr/bin:/bin:/usr/sbin:/sbin. These are what would resolve a user's
// own install if the bundled build were ever absent.
const CANDIDATE_PATHS = process.platform === 'win32'
  ? [
      path.join(os.homedir(), 'go', 'bin', BINARY),
      path.join(process.env.LOCALAPPDATA ?? '', 'Programs', 'workbook', BINARY),
      path.join(process.env.ProgramFiles ?? '', 'workbook', BINARY)
    ].filter((candidate) => !candidate.startsWith(path.sep) || candidate.length > 1)
  : [
      path.join(os.homedir(), '.local', 'bin', BINARY),
      path.join(os.homedir(), 'go', 'bin', BINARY),
      '/opt/homebrew/bin/workbook',
      '/usr/local/bin/workbook'
    ]

let cachedBinary = null

async function isExecutable (candidate) {
  try {
    await fs.access(candidate, fs.constants.X_OK)
    return true
  } catch {
    return false
  }
}

/**
 * Resolve the workbook binary once per process.
 * @param {string} [override] explicit path from settings
 * @returns {Promise<string>}
 */
async function resolveBinary (override) {
  if (override) {
    if (!(await isExecutable(override))) {
      throw new Error(`configured workbook binary is not executable: ${override}`)
    }
    return override
  }
  if (cachedBinary) return cachedBinary

  // The build shipped inside the app wins.
  //
  // Workbench builds Workbook from source at package time and bundles the
  // result, so an install of the app is an install of a known, matching CLI —
  // no separate install step, and no dependence on whatever version happens to
  // be on the machine. Preferring it also makes the app's behaviour a property
  // of the app rather than of the host, which is what makes a bug report
  // reproducible.
  //
  // Someone who wants the app to drive their own build passes an override; that
  // still wins over everything here.
  if (process.resourcesPath) {
    const bundled = path.join(process.resourcesPath, BINARY)
    if (await isExecutable(bundled)) {
      cachedBinary = bundled
      return cachedBinary
    }
  }

  // No bundled copy: a development run, or a build staged without one. Fall back
  // to whatever is installed.
  //
  // Walked directly rather than through a shell: `command -v` would need
  // shell:true, which concatenates rather than escapes its arguments.
  for (const directory of (process.env.PATH ?? '').split(path.delimiter)) {
    if (!directory) continue
    const candidate = path.join(directory, BINARY)
    if (await isExecutable(candidate)) {
      cachedBinary = candidate
      return cachedBinary
    }
  }

  for (const candidate of CANDIDATE_PATHS) {
    if (await isExecutable(candidate)) {
      cachedBinary = candidate
      return cachedBinary
    }
  }

  throw new Error(
    'workbook binary not found. Build one into the app with `npm run stage`, ' +
    'or install it on PATH.'
  )
}

/**
 * Run a workbook command that emits the JSON envelope and return its `data`.
 *
 * Errors carry the CLI's own category and message when it produced an error
 * document, because those are more precise than an exit code alone.
 *
 * @param {string} cwd repository to run inside
 * @param {string[]} args arguments after the binary, excluding --json
 * @param {{binary?: string}} [options]
 */
async function runJSON (cwd, args, options = {}) {
  const binary = await resolveBinary(options.binary)
  const argv = args.includes('--json') ? args : [...args, '--json']

  const { stdout, stderr, code } = await run(binary, argv, cwd)

  const parse = (text) => {
    if (!text.trim()) return null
    try {
      return JSON.parse(text)
    } catch {
      return null
    }
  }

  if (code !== 0) {
    // A failure writes a `workbook.error` document to stderr and exits with the
    // code for its category — 5 for validation, for instance — leaving stdout
    // empty. Reading the envelope from stderr is what turns that into a
    // sentence worth showing a user; without it the message is raw JSON.
    const envelope = parse(stderr)
    const detail = envelope?.error
      ? envelope.error.message
      : stderr.trim() || `workbook ${argv[0]} exited ${code}`
    const error = new Error(detail)
    error.exitCode = code
    error.category = envelope?.error?.category
    throw error
  }

  const envelope = parse(stdout)
  if (!envelope) {
    throw new Error(`workbook ${argv[0]} produced no JSON: ${stderr.trim()}`)
  }
  return envelope.data
}

function run (binary, argv, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, argv, { cwd, env: process.env })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (chunk) => { stdout += chunk })
    child.stderr.on('data', (chunk) => { stderr += chunk })
    child.on('error', reject)
    child.on('close', (code) => resolve({ stdout, stderr, code }))
  })
}

/**
 * Read the binary's version, as a smoke test that it runs at all.
 *
 * The resolved path travels with it: which Workbook is driving a repository is
 * the first thing worth knowing when the app and a terminal disagree, and it
 * should never require reading source to find out.
 */
async function version (binaryOverride) {
  const binary = await resolveBinary(binaryOverride)
  const data = await runJSON(os.homedir(), ['version'], { binary })
  return { ...data, path: binary, bundled: Boolean(
    process.resourcesPath && binary.startsWith(process.resourcesPath)
  ) }
}

/**
 * Bootstrap Workbook in a repository.
 *
 * --no-sync is deliberate: importing a repository must not push refs to a
 * remote as a side effect of adding it to a list. Sync is a per-project choice
 * the user makes afterwards, from the board.
 */
async function setup (repoPath, key, { binary, sync = false } = {}) {
  const args = ['setup', '--key', key]
  if (!sync) args.push('--no-sync')
  return runJSON(repoPath, args, { binary })
}

// Workbook's exit code for "somebody else holds this task and you do not".
// The write is refused and nothing is recorded; --force records yours beside
// theirs. It is a code rather than a message because it is a decision for the
// caller, not an error.
const EXIT_ASSIGNED = 10

/**
 * Assign a task to an email address, or to `self`.
 *
 * One assignment per invocation is the CLI's rule, so this takes one principal.
 * `taken` comes back instead of an error when the task is already held by
 * somebody else: the caller decides whether to record theirs alongside, because
 * that is a question about people rather than about software.
 *
 * @param {{force?: boolean, binary?: string}} [options]
 */
async function assign (repoPath, taskId, principal, options = {}) {
  const args = ['update', taskId, '--assign', principal]
  if (options.force) args.push('--force')
  try {
    return { ok: true, data: await runJSON(repoPath, args, options) }
  } catch (error) {
    if (error.exitCode === EXIT_ASSIGNED) {
      return { ok: false, taken: true, message: error.message }
    }
    throw error
  }
}

/**
 * Remove an assignment.
 *
 * Workbook allows this only for the person the assignment names or the person
 * who recorded it, and that refusal arrives as an ordinary error — it is a rule
 * about who may act, so it is reported rather than worked around.
 */
async function unassign (repoPath, taskId, principal, options = {}) {
  return runJSON(repoPath, ['update', taskId, '--unassign', principal], options)
}

/**
 * The statuses this project defines.
 *
 * Per project, not global: Workbook lets a project rename, reorder and retire
 * its own columns, so the choices a task can move between are the choices that
 * project actually has.
 */
async function listStatuses (repoPath, options = {}) {
  const data = await runJSON(repoPath, ['status', 'list'], options)
  return {
    default: data.default ?? null,
    statuses: (data.statuses ?? []).map((status) => ({
      status: status.status,
      label: status.label ?? status.status,
      tags: status.tags ?? [],
      order: status.order ?? 0
    }))
  }
}

/** Move a task to a status. An unknown one is refused, not guessed at. */
async function setStatus (repoPath, taskId, status, options = {}) {
  return runJSON(repoPath, ['update', taskId, '--status', status], options)
}

/** Every live task in a repository. */
async function listTasks (repoPath, { binary } = {}) {
  return runJSON(repoPath, ['list'], { binary })
}

module.exports = {
  resolveBinary, runJSON, version, setup, listTasks, assign, unassign,
  listStatuses, setStatus, BINARY, EXIT_ASSIGNED
}
