'use strict'

// Scanning a folder for Git repositories worth importing.

const fs = require('node:fs/promises')
const path = require('node:path')

// Directories that never contain a repository the user means to track. Walking
// into node_modules on a large tree costs seconds and finds only dependencies.
const SKIP_DIRECTORIES = new Set([
  'node_modules', 'vendor', 'dist', 'build', 'out', 'target',
  '.venv', 'venv', '__pycache__', '.next', '.nuxt', '.cache',
  // macOS
  'Library', 'Applications', '.Trash',
  // Windows: scanning these from a home directory costs minutes and finds
  // nothing a user means to track.
  'AppData', 'Program Files', 'Program Files (x86)', 'Windows',
  '$Recycle.Bin', 'OneDrive', 'ProgramData',
  // Linux
  'snap', '.cargo', '.rustup', '.nvm'
])

const DEFAULT_MAX_DEPTH = 4

// Workbook's own constraint, from internal/core/id.go: an uppercase letter
// followed by 1-9 uppercase alphanumerics. Enforced here so the import wizard
// can refuse a bad key before minting an identity that cannot be renamed.
const KEY_PATTERN = /^[A-Z][A-Z0-9]{1,9}$/

function isValidKey (key) {
  return KEY_PATTERN.test(key)
}

/**
 * Propose a project key from a directory name.
 *
 * The suggestion matters more than it looks: a project key is immutable once
 * minted, and changing it later means deleting refs/workbook/project and
 * refs/workbook/config, then removing .git/workbook and .workbook by hand. The
 * wizard shows this and lets the user edit it before anything is written.
 */
function suggestKey (repoPath, taken = new Set()) {
  const name = path.basename(repoPath)

  // camelCase and kebab/snake boundaries both read as word starts.
  const words = name
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .split(/[^A-Za-z0-9]+/)
    .filter(Boolean)

  let base = ''
  if (words.length >= 2) {
    base = words.map((word) => word[0]).join('').toUpperCase()
  } else if (words.length === 1) {
    base = words[0].slice(0, 4).toUpperCase()
  }
  base = base.replace(/[^A-Z0-9]/g, '')
  if (base.length < 2) base = (base + 'WB').slice(0, 2)
  base = base.slice(0, 10)
  if (!/^[A-Z]/.test(base)) base = `W${base}`.slice(0, 10)

  if (!taken.has(base)) return base
  for (let suffix = 2; suffix < 100; suffix += 1) {
    const candidate = `${base.slice(0, 10 - String(suffix).length)}${suffix}`
    if (!taken.has(candidate)) return candidate
  }
  return base
}

async function readJSON (file) {
  try {
    return JSON.parse(await fs.readFile(file, 'utf8'))
  } catch {
    return null
  }
}

async function exists (target) {
  try {
    await fs.access(target)
    return true
  } catch {
    return false
  }
}

/**
 * Describe one repository: whether Workbook is already set up, and under what
 * identity.
 *
 * A checkout can carry the identity in refs/workbook/project without a
 * .workbook/config.json — the CLI warns about exactly that case — so the
 * absence of the file is reported as "unknown", not as "not set up".
 */
async function inspectRepository (repoPath) {
  const config = await readJSON(path.join(repoPath, '.workbook', 'config.json'))
  const hasWorkbookDirectory = await exists(path.join(repoPath, '.workbook'))
  return {
    path: repoPath,
    name: path.basename(repoPath),
    initialized: Boolean(config),
    partiallyInitialized: !config && hasWorkbookDirectory,
    key: config?.key ?? null,
    projectId: config?.projectId ?? null
  }
}

/**
 * Walk `root` and return every Git repository under it.
 *
 * Repositories are not descended into: a checkout with submodules or a nested
 * clone is imported as one project, which is what the per-repository storage
 * model means by a project.
 */
async function scan (root, { maxDepth = DEFAULT_MAX_DEPTH } = {}) {
  const found = []

  async function walk (directory, depth) {
    if (depth > maxDepth) return

    let entries
    try {
      entries = await fs.readdir(directory, { withFileTypes: true })
    } catch {
      return // Unreadable directory: skip rather than fail the whole scan.
    }

    if (entries.some((entry) => entry.name === '.git')) {
      found.push(await inspectRepository(directory))
      return
    }

    for (const entry of entries) {
      if (!entry.isDirectory() || entry.isSymbolicLink()) continue
      if (entry.name.startsWith('.') || SKIP_DIRECTORIES.has(entry.name)) continue
      await walk(path.join(directory, entry.name), depth + 1)
    }
  }

  await walk(root, 0)

  const taken = new Set(found.filter((repo) => repo.key).map((repo) => repo.key))
  for (const repo of found) {
    repo.suggestedKey = repo.key ?? suggestKey(repo.path, taken)
    if (!repo.key) taken.add(repo.suggestedKey)
    repo.relativePath = path.relative(root, repo.path) || path.basename(repo.path)
  }

  found.sort((a, b) => a.relativePath.localeCompare(b.relativePath))
  return found
}

module.exports = { scan, inspectRepository, suggestKey, isValidKey, KEY_PATTERN }
