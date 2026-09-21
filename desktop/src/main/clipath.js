'use strict'

// Putting the bundled `workbook` CLI on the user's PATH.
//
// The desktop app carries its own copy of Workbook (see workbook.js) so it
// never depends on what a user happens to have installed, but that means a
// terminal sitting next to the app cannot run `workbook` at all unless
// something puts a copy somewhere PATH already looks. This module is that
// something: on launch it copies the bundled binary into a directory the app
// owns and adds that directory to PATH — POSIX shell profiles and fish's own
// config on macOS/Linux, the user's registry PATH on Windows.
//
// No `require('electron')` anywhere in this file: scripts/check-shell.js loads
// every module in this directory outside Electron to catch a broken export,
// and every input here (platform, env, home, the bundled binary's path, a
// command runner, an existence check) arrives as an argument with a real
// default. That is also what lets the tests run entirely against a temporary
// HOME instead of the machine's own.

const crypto = require('node:crypto')
const fsSync = require('node:fs')
const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { execFile } = require('node:child_process')

const { BINARY } = require('./workbook')

// Distinct from setup-dev-env.sh's `# >>> workbook development environment >>>`
// so a machine that has run both the dev script and the packaged app keeps two
// independent blocks instead of one clobbering the other on every write.
const MARK_BEGIN = '# >>> workbench app PATH >>>'
const MARK_END = '# <<< workbench app PATH <<<'

// ---------------------------------------------------------------------------
// 1. Where it goes.
// ---------------------------------------------------------------------------

/**
 * The directory the app copies its CLI into and adds to PATH.
 *
 * Derived from `home`/`env` rather than from Electron's `app.getPath`, even
 * though on a case-insensitive macOS volume this lands inside the app's own
 * userData directory (app.getName() is "workbench") — deriving it from plain
 * arguments is what lets a test point the whole module at a temp directory
 * without ever touching Electron.
 */
function installDirectory ({ platform = process.platform, env = process.env, home = os.homedir() } = {}) {
  if (platform === 'darwin') {
    return path.join(home, 'Library', 'Application Support', 'Workbench', 'bin')
  }
  if (platform === 'win32') {
    const base = env.LOCALAPPDATA || path.join(home, 'AppData', 'Local')
    return path.join(base, 'Workbench', 'bin')
  }
  const dataHome = env.XDG_DATA_HOME || path.join(home, '.local', 'share')
  return path.join(dataHome, 'workbench', 'bin')
}

async function pathExists (target) {
  try {
    await fs.access(target)
    return true
  } catch {
    return false
  }
}

// ---------------------------------------------------------------------------
// 2. Keeping the copy current.
// ---------------------------------------------------------------------------

async function sha256 (file) {
  const hash = crypto.createHash('sha256')
  for await (const chunk of fsSync.createReadStream(file)) hash.update(chunk)
  return hash.digest('hex')
}

/**
 * Whether two files hold the same bytes.
 *
 * Size first because it is nearly free and settles almost every comparison;
 * sha256 only when sizes agree. mtime is never consulted — the bundle's mtime
 * travels with however it was downloaded or extracted and says nothing about
 * what changed inside it.
 */
async function filesEqual (a, b) {
  const [sa, sb] = await Promise.all([fs.stat(a), fs.stat(b)])
  if (sa.size !== sb.size) return false
  const [ha, hb] = await Promise.all([sha256(a), sha256(b)])
  return ha === hb
}

/**
 * Copy `bundled` into `directory` if it is missing or different there.
 *
 * Callers are expected to have already confirmed `bundled` exists — install()
 * does, before it ever creates `directory` — so a missing bundle is a bug at
 * the call site, not a case this function absorbs quietly.
 *
 * @returns {Promise<{path: string, copied: boolean, reason: 'missing'|'differs'|'current'}>}
 */
async function syncBinary ({ bundled, directory }) {
  await fs.mkdir(directory, { recursive: true })
  // Named BINARY rather than after whatever `bundled` is called, so this is
  // the one place that decides what the installed copy is named — the same
  // reason workbook.js exports BINARY instead of every caller inventing it.
  const destination = path.join(directory, BINARY)

  let reason
  try {
    reason = (await filesEqual(bundled, destination)) ? 'current' : 'differs'
  } catch (error) {
    if (error.code !== 'ENOENT') throw error
    reason = 'missing'
  }

  if (reason === 'current') {
    return { path: destination, copied: false, reason }
  }

  // Atomic copy: land the bytes at a temp name in the same directory (so the
  // final rename is same-filesystem and instantaneous), fix the executable bit
  // — an upstream copy can lose it, which is why scripts/after-pack.js chmods
  // for the same reason — and only then rename over the real name. A reader of
  // resolveBinary() in workbook.js can never observe a half-written file.
  const temp = path.join(directory, `.workbook.${process.pid}.${crypto.randomBytes(4).toString('hex')}.tmp`)
  try {
    await fs.copyFile(bundled, temp)
    await fs.chmod(temp, 0o755)
    await fs.rename(temp, destination)
  } catch (error) {
    await fs.unlink(temp).catch(() => {})
    throw error
  }
  return { path: destination, copied: true, reason }
}

// ---------------------------------------------------------------------------
// 3. The marked block.
// ---------------------------------------------------------------------------

/**
 * A POSIX profile fragment that appends `directory` to PATH once.
 *
 * Mirrors setup-dev-env.sh's own `case` guard, but appends rather than
 * prepends: prepending would let the app's own copy shadow a Homebrew install
 * the user chose deliberately, and the ruling here is that Homebrew keeps
 * winning. Both the pattern and the assignment quote the directory, so a space
 * (the default macOS path has one, in "Application Support") or a glob
 * character in it is literal rather than re-interpreted by the shell.
 */
function posixBlock (directory) {
  const lines = [
    MARK_BEGIN,
    'case ":${PATH}:" in',
    `\t*":${directory}:"*) ;;`,
    `\t*) PATH="\${PATH}:${directory}" ;;`,
    'esac',
    'export PATH',
    MARK_END
  ]
  return `${lines.join('\n')}\n`
}

/**
 * The same idea in fish syntax.
 *
 * fish is not POSIX — `PATH="${PATH}:x"` is a syntax error there — so it needs
 * its own block rather than sharing posixBlock's text. setup-dev-env.sh never
 * writes this: it only touches .bashrc/.zshrc/.profile, which leaves an owner
 * whose shell is fish with nothing on PATH at all, hence this second builder.
 */
function fishBlock (directory) {
  const lines = [
    MARK_BEGIN,
    `if not contains "${directory}" $PATH`,
    `    set -gx PATH $PATH "${directory}"`,
    'end',
    MARK_END
  ]
  return `${lines.join('\n')}\n`
}

/**
 * Remove a previous marked block from `text`, in place of the lines it
 * occupied.
 *
 * Same shape as setup-dev-env.sh's awk: a begin marker with no matching end
 * (only possible from a hand-edited profile) swallows every line after it,
 * because there is no signal left to say where the block was meant to stop.
 */
function stripBlock (text) {
  if (!text) return ''
  let skipping = false
  const kept = []
  for (const line of text.split('\n')) {
    if (line === MARK_BEGIN) skipping = true
    if (!skipping) kept.push(line)
    if (line === MARK_END) skipping = false
  }
  return kept.join('\n')
}

/**
 * Write `block` into `file`, replacing any previous marked block in place.
 *
 * @returns {Promise<{file: string, changed: boolean}>}
 */
async function writeBlock (file, block) {
  const directory = path.dirname(file)

  let existing = ''
  try {
    existing = await fs.readFile(file, 'utf8')
  } catch (error) {
    if (error.code !== 'ENOENT') throw error
  }

  const kept = stripBlock(existing)
  // A newline is owed before the block only when the kept text is non-empty
  // and does not already end in one — a profile with no trailing newline (a
  // hand-typed one-liner, say) must not have its last line glued to the block.
  const next = kept.length === 0 ? block : (kept.endsWith('\n') ? kept : `${kept}\n`) + block

  // Nothing to do: a second launch must not rewrite a profile it already
  // wrote, which matters because rewriting it would also strip and reapply
  // the mode/owner-preserving copy below for no reason.
  if (next === existing) return { file, changed: false }

  await fs.mkdir(directory, { recursive: true })
  const temp = path.join(directory, `.workbook-profile.${process.pid}.${crypto.randomBytes(4).toString('hex')}.tmp`)
  await fs.writeFile(temp, next)
  try {
    // copyFile rather than rename: a profile that already exists keeps its own
    // mode and owner this way, exactly as setup-dev-env.sh's write_path_block
    // ends with `cat` instead of `mv` for the same reason.
    await fs.copyFile(temp, file)
  } finally {
    await fs.unlink(temp).catch(() => {})
  }
  return { file, changed: true }
}

// ---------------------------------------------------------------------------
// 4. Which profiles.
// ---------------------------------------------------------------------------

/**
 * The profile files that should carry the block, in the order they are
 * written.
 *
 * `exists` is injected (a synchronous predicate) rather than reached for via
 * fs directly, so a test can answer it from a fake filesystem without a real
 * HOME existing at all.
 */
function profileTargets ({ platform = process.platform, env = process.env, home = os.homedir(), exists }) {
  if (platform === 'win32') return [] // The registry is the whole story there.

  const targets = []

  // Mirrors setup-dev-env.sh exactly: every one of the three that exists, or
  // just .profile (to be created) when none do.
  const posixCandidates = ['.bashrc', '.zshrc', '.profile'].map((name) => path.join(home, name))
  const existingPosix = posixCandidates.filter((file) => exists(file))
  if (existingPosix.length > 0) {
    for (const file of existingPosix) targets.push({ file, syntax: 'posix' })
  } else {
    targets.push({ file: path.join(home, '.profile'), syntax: 'posix' })
  }

  // Added only when fish is indicated — either its config directory already
  // exists, or the login shell says so — because the app has no business
  // inventing a fish configuration for someone who has never used fish.
  const configHome = env.XDG_CONFIG_HOME || path.join(home, '.config')
  const fishDirectory = path.join(configHome, 'fish')
  const usesFish = exists(fishDirectory) || /fish$/.test(env.SHELL || '')
  if (usesFish) {
    targets.push({ file: path.join(fishDirectory, 'config.fish'), syntax: 'fish' })
  }

  return targets
}

// ---------------------------------------------------------------------------
// 5. Windows.
// ---------------------------------------------------------------------------

function defaultRun (file, args) {
  return new Promise((resolve) => {
    execFile(file, args, (error, stdout, stderr) => {
      // execFile's callback error carries a numeric exitCode; a launch failure
      // (ENOENT, say) has none, so it is reported as a generic non-zero code
      // rather than thrown — every caller here already branches on `code`.
      const code = error ? (typeof error.code === 'number' ? error.code : 1) : 0
      resolve({ code, stdout: stdout ?? '', stderr: stderr ?? '' })
    })
  })
}

// `reg query`'s output is indented, human-formatted text, e.g.:
//   HKEY_CURRENT_USER\Environment
//       Path    REG_EXPAND_SZ    C:\Existing;C:\Other
function parseRegQuery (stdout) {
  const match = stdout.match(/^\s*Path\s+(REG_SZ|REG_EXPAND_SZ)\s+(.*)$/im)
  if (!match) return null
  return { type: match[1], value: match[2].trim() }
}

/**
 * Add `directory` to the current user's PATH in the registry, if it is not
 * there already.
 *
 * Never invoked on a non-Windows host. `setx` is deliberately avoided — it
 * truncates a PATH over 1024 characters and has destroyed real user PATHs —
 * in favor of `reg add`, which also lets the existing value's type
 * (REG_EXPAND_SZ vs REG_SZ) be preserved so a `%USERPROFILE%`-style entry
 * already in there keeps expanding instead of becoming a literal string.
 */
async function updateWindowsPath ({ directory, run = defaultRun }) {
  const query = await run('reg', ['query', 'HKCU\\Environment', '/v', 'Path'])
  const parsed = query.code === 0 ? parseRegQuery(query.stdout) : null
  const currentValue = parsed?.value ?? ''
  const type = parsed?.type ?? 'REG_SZ'

  const segments = currentValue.split(';').map((segment) => segment.trim()).filter(Boolean)
  const already = segments.some((segment) => segment.toLowerCase() === directory.toLowerCase())
  if (already) return { changed: false }

  const nextValue = currentValue ? `${currentValue};${directory}` : directory
  // No shell involved (run wraps execFile, not exec), so nextValue travels as
  // one argv entry regardless of spaces — the double quotes the design shows
  // are how a human would type this at a prompt, not something this call
  // needs to add itself.
  const add = await run('reg', ['add', 'HKCU\\Environment', '/v', 'Path', '/t', type, '/d', nextValue, '/f'])
  if (add.code !== 0) {
    throw new Error(`reg add failed (${add.code}): ${add.stderr || add.stdout}`)
  }
  return { changed: true }
}

// ---------------------------------------------------------------------------
// 6. The orchestrator.
// ---------------------------------------------------------------------------

/**
 * Copy the bundled CLI into place and put it on PATH.
 *
 * Never throws: every step's failure is caught into `errors` and the rest
 * still runs, because a read-only `.zshrc` must not cost the user the binary
 * copy, and a broken PATH update must never be the reason the app fails to
 * open.
 */
async function install ({
  platform = process.platform,
  env = process.env,
  home = os.homedir(),
  bundled,
  run,
  exists = fsSync.existsSync
} = {}) {
  const result = {
    skipped: false,
    directory: null,
    binary: null,
    copied: false,
    reason: null,
    profiles: [],
    windows: null,
    changed: false,
    errors: []
  }

  // An explicit escape hatch, checked first and unconditionally: any
  // non-empty value opts out, no matter what else is true.
  if (env.WORKBENCH_SKIP_PATH_SETUP) {
    return { ...result, skipped: true, reason: 'WORKBENCH_SKIP_PATH_SETUP is set' }
  }

  // A development run (`npm start`) has no `workbook` under
  // process.resourcesPath, so it has nothing to copy — which is also what
  // keeps a development run from ever touching the developer's own fish
  // config or shell profiles.
  if (!bundled || !(await pathExists(bundled))) {
    return { ...result, skipped: true, reason: 'no bundled binary to copy' }
  }

  const directory = installDirectory({ platform, env, home })
  result.directory = directory

  try {
    const sync = await syncBinary({ bundled, directory })
    result.binary = sync.path
    result.copied = sync.copied
    result.reason = sync.reason
    if (sync.copied) result.changed = true
  } catch (error) {
    result.errors.push(`copying the CLI: ${error.message}`)
  }

  if (platform === 'win32') {
    try {
      const outcome = await updateWindowsPath({ directory, run })
      result.windows = outcome
      if (outcome.changed) result.changed = true
    } catch (error) {
      result.errors.push(`updating the Windows PATH: ${error.message}`)
    }
    return result
  }

  for (const target of profileTargets({ platform, env, home, exists })) {
    try {
      const block = target.syntax === 'fish' ? fishBlock(directory) : posixBlock(directory)
      const outcome = await writeBlock(target.file, block)
      result.profiles.push(outcome)
      if (outcome.changed) result.changed = true
    } catch (error) {
      result.errors.push(`updating ${target.file}: ${error.message}`)
    }
  }

  return result
}

module.exports = {
  installDirectory,
  syncBinary,
  posixBlock,
  fishBlock,
  writeBlock,
  profileTargets,
  updateWindowsPath,
  install,
  MARK_BEGIN,
  MARK_END
}
