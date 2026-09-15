'use strict'

// What a repository is, beyond its path: which stacks it uses, who works on
// it, and when it was last touched.
//
// This is what makes an import list choosable. Twelve rows of directory names
// tell you nothing about which ones you actually want; "React + Firebase, you,
// three days ago" does.

const { execFile } = require('node:child_process')
const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')

// A runaway git call must not hang a scan of a hundred repositories, and no
// answer here is worth waiting on: every field this module produces is
// decoration that the row degrades gracefully without.
const GIT_TIMEOUT_MS = 5000

// Bounded so that a shortlog over a repository with a hundred thousand commits
// costs the same as one over a hundred.
const AUTHOR_SAMPLE = 300

/**
 * Run one git command, returning trimmed stdout or null.
 *
 * Never throws: a bare repository, a repository with no commits, a missing
 * remote and a corrupt object store all reach here as ordinary failures, and
 * all of them mean the same thing to a caller — this field has no value.
 */
function git (cwd, args) {
  return new Promise((resolve) => {
    execFile('git', args, { cwd, timeout: GIT_TIMEOUT_MS, maxBuffer: 4 << 20 },
      (error, stdout) => resolve(error ? null : stdout.trim()))
  })
}

// Manifests and config files that name a stack, most specific first: a
// Next.js repository also has a package.json, and "Next.js" is the more useful
// of the two answers.
const STACK_FILES = [
  ['next.config.js', 'Next.js'], ['next.config.mjs', 'Next.js'], ['next.config.ts', 'Next.js'],
  ['nuxt.config.ts', 'Nuxt'], ['svelte.config.js', 'Svelte'], ['astro.config.mjs', 'Astro'],
  ['vite.config.ts', 'Vite'], ['vite.config.js', 'Vite'],
  ['tailwind.config.js', 'Tailwind'], ['tailwind.config.ts', 'Tailwind'],
  ['firebase.json', 'Firebase'], ['firestore.rules', 'Firebase'],
  ['go.mod', 'Go'], ['Cargo.toml', 'Rust'], ['pyproject.toml', 'Python'],
  ['requirements.txt', 'Python'], ['Pipfile', 'Python'], ['setup.py', 'Python'],
  ['Gemfile', 'Ruby'], ['composer.json', 'PHP'], ['pubspec.yaml', 'Flutter'],
  ['Package.swift', 'Swift'], ['build.gradle', 'Gradle'], ['build.gradle.kts', 'Gradle'],
  ['pom.xml', 'Maven'], ['CMakeLists.txt', 'CMake'], ['Makefile', 'Make'],
  ['Dockerfile', 'Docker'], ['docker-compose.yml', 'Docker'],
  ['project.godot', 'Godot'], ['Chart.yaml', 'Helm'], ['terraform.tf', 'Terraform'],
  ['deno.json', 'Deno'], ['bun.lockb', 'Bun']
]

// Dependency names worth naming as a stack when a package.json carries them.
const NODE_DEPENDENCIES = [
  ['react', 'React'], ['vue', 'Vue'], ['svelte', 'Svelte'], ['@angular/core', 'Angular'],
  ['electron', 'Electron'], ['express', 'Express'], ['next', 'Next.js'],
  ['typescript', 'TypeScript'], ['tailwindcss', 'Tailwind'], ['vite', 'Vite'],
  ['zustand', 'Zustand'], ['firebase', 'Firebase'], ['firebase-admin', 'Firebase'],
  ['three', 'Three.js'], ['discord.js', 'Discord.js'], ['fastify', 'Fastify']
]

// Workspace directories are the normal shape of these repositories: the React
// app in frontend/, the Cloud Functions in functions/. Reading only the root
// reports such a repository as "Firebase, Node" and misses what it actually is.
const SUBDIRECTORY_SKIP = new Set([
  'node_modules', 'vendor', 'dist', 'build', 'out', 'target', 'docs', 'assets',
  'public', 'static', 'test', 'tests', '__tests__', 'coverage', 'tmp', 'scripts'
])

// Bounded so a repository with fifty top-level directories costs the same as
// one with five.
const MAX_SUBDIRECTORIES = 12

async function readJSON (file) {
  try {
    return JSON.parse(await fs.readFile(file, 'utf8'))
  } catch {
    return null
  }
}

/**
 * Identify the stacks a repository uses, from its root and one level below.
 *
 * Only one level: a deep search would find every dependency's own manifest and
 * report a Node project as forty stacks. One level is what distinguishes a
 * monorepo's actual stack — the React app in frontend/ — from the Firebase
 * config that happens to sit at its root.
 */
async function detectStacks (repoPath, entries) {
  const stacks = []
  const add = (stack) => { if (stack && !stacks.includes(stack)) stacks.push(stack) }

  async function scanDirectory (directory, names) {
    const present = new Set(names)
    for (const [file, stack] of STACK_FILES) {
      if (present.has(file)) add(stack)
    }
    if (present.has('tsconfig.json')) add('TypeScript')

    if (!present.has('package.json')) return false
    const manifest = await readJSON(path.join(directory, 'package.json'))
    const dependencies = {
      ...(manifest?.dependencies ?? {}),
      ...(manifest?.devDependencies ?? {})
    }
    let matched = false
    for (const [dependency, stack] of NODE_DEPENDENCIES) {
      if (dependencies[dependency]) { add(stack); matched = true }
    }
    return matched
  }

  const rootMatched = await scanDirectory(repoPath, entries)

  const subdirectories = []
  for (const name of entries) {
    if (subdirectories.length >= MAX_SUBDIRECTORIES) break
    if (name.startsWith('.') || SUBDIRECTORY_SKIP.has(name)) continue
    subdirectories.push(name)
  }

  let nestedMatched = false
  await Promise.all(subdirectories.map(async (name) => {
    const directory = path.join(repoPath, name)
    try {
      const stat = await fs.stat(directory)
      if (!stat.isDirectory()) return
      const names = (await fs.readdir(directory)).slice(0, 200)
      if (await scanDirectory(directory, names)) nestedMatched = true
    } catch {
      // Unreadable subdirectory: nothing to learn, nothing to fail over.
    }
  }))

  // "Node" only when nothing more specific turned up anywhere; "React, Node"
  // says less than "React".
  if (!rootMatched && !nestedMatched && stacks.length === 0 &&
      new Set(entries).has('package.json')) {
    add('Node')
  }
  return stacks.slice(0, 6)
}

function shortDate (iso) {
  if (!iso) return null
  const then = new Date(iso)
  if (Number.isNaN(then.getTime())) return null
  const days = Math.floor((Date.now() - then.getTime()) / 86400000)
  if (days <= 0) return 'today'
  if (days === 1) return 'yesterday'
  if (days < 30) return `${days}d ago`
  if (days < 365) return `${Math.floor(days / 30)}mo ago`
  return `${Math.floor(days / 365)}y ago`
}

/**
 * Read everything git can cheaply say about one repository.
 *
 * Each field is independent: a repository with no commits still reports its
 * remote, and one with no remote still reports its authors.
 */
async function readGitInfo (repoPath) {
  const [revParse, lastCommit, remote, identity, authorSample] =
    await Promise.all([
    // One invocation answers both: the output is two lines, bareness then HEAD.
    git(repoPath, ['rev-parse', '--is-bare-repository', '--abbrev-ref', 'HEAD']),
    git(repoPath, ['log', '-1', '--format=%aI%x00%an%x00%s']),
    git(repoPath, ['remote', 'get-url', 'origin']),
    // And one for both identity keys, rather than one per key.
    git(repoPath, ['config', '--get-regexp', '^user\\.(email|name)$']),
    // Name and email together: the email is the identity Workbook assigns
    // against, and the name is the only thing worth showing a human.
    git(repoPath, ['log', `-n${AUTHOR_SAMPLE}`, '--format=%an%x1f%ae'])
  ])

  const [bare, head] = (revParse ?? '').split('\n')

  // `config --get-regexp` prints "key value" per line, in no fixed order.
  let configuredEmail = null
  let configuredName = null
  for (const line of (identity ?? '').split('\n')) {
    const separator = line.indexOf(' ')
    if (separator === -1) continue
    const key = line.slice(0, separator)
    const value = line.slice(separator + 1).trim()
    if (key === 'user.email') configuredEmail = value
    if (key === 'user.name') configuredName = value
  }

  const [committedAt, lastAuthor, subject] = (lastCommit ?? '').split('\0')

  // Counted from a bounded sample, so this is "who has been working on it
  // lately", not a lifetime tally — which is the more useful answer anyway.
  //
  // Keyed by email because that is the identity that matters: one person
  // commits as "Tyler" and "tyler kilgore" from the same address, and counting
  // those separately would invent two people.
  const counts = new Map()
  for (const line of (authorSample ?? '').split('\n')) {
    if (!line.trim()) continue
    const [name, email] = line.split('\u001f')
    const key = (email ?? '').trim().toLowerCase()
    if (!key) continue
    const entry = counts.get(key) ?? { email: key, names: new Map(), commits: 0 }
    entry.commits += 1
    const display = (name ?? '').trim()
    if (display) entry.names.set(display, (entry.names.get(display) ?? 0) + 1)
    counts.set(key, entry)
  }

  const allAuthors = [...counts.values()]
    .map((entry) => ({
      email: entry.email,
      // The spelling they use most often, not the first one seen.
      name: [...entry.names.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? entry.email,
      commits: entry.commits
    }))
    .sort((a, b) => b.commits - a.commits)

  const authors = allAuthors.slice(0, 3)

  return {
    bare: bare === 'true',
    // Every contributor in the sample, for the people directory. The row only
    // shows the top few, but the directory wants them all.
    allAuthors,
    // Workbook records an assignment against this checkout's user.email, so a
    // repository configured with a different one assigns to a different person.
    configuredEmail: configuredEmail || null,
    configuredName: configuredName || null,
    branch: head && head !== 'HEAD' ? head : null,
    detached: head === 'HEAD',
    remote: remote ?? null,
    lastCommitAt: committedAt || null,
    lastCommitRelative: shortDate(committedAt),
    lastCommitSubject: subject || null,
    lastCommitAuthor: lastAuthor || null,
    authors,
    // A repository with no commits has nothing to import a history from, and
    // saying so is more useful than an empty row.
    empty: !lastCommit
  }
}

/** Everything about one repository, for one row of the import list. */
async function describe (repoPath) {
  let entries = []
  try {
    entries = (await fs.readdir(repoPath, { withFileTypes: true })).map((e) => e.name)
  } catch {
    // Unreadable root: git may still answer, so carry on with no stacks.
  }
  const [gitInfo, stacks] = await Promise.all([
    readGitInfo(repoPath),
    detectStacks(repoPath, entries)
  ])
  return { ...gitInfo, stacks }
}

/**
 * Describe many repositories with a bounded number in flight.
 *
 * Unbounded, a scan of a large tree would fork several git processes per
 * repository all at once; bounded, the whole scan costs a predictable amount
 * and finishes sooner because nothing is competing.
 */
async function describeAll (repoPaths, { concurrency = 8, onProgress } = {}) {
  const results = new Map()
  let index = 0
  let done = 0

  const workers = Array.from({ length: Math.min(concurrency, repoPaths.length) }, async () => {
    while (index < repoPaths.length) {
      const current = repoPaths[index++]
      try {
        results.set(current, await describe(current))
      } catch {
        results.set(current, null) // One bad repository must not fail the scan.
      }
      done += 1
      onProgress?.({ done, total: repoPaths.length })
    }
  })

  await Promise.all(workers)
  return results
}

/**
 * The identity git would use outside any repository.
 *
 * This is what `workbook --assign self` records in a checkout that sets no
 * user.email of its own, which makes it the right seed for "me".
 */
async function globalGitEmail () {
  const email = await git(os.homedir(), ['config', '--global', 'user.email'])
  return email ? email.trim().toLowerCase() : null
}

module.exports = {
  describe, describeAll, detectStacks, readGitInfo, shortDate, globalGitEmail
}
