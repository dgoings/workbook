'use strict'

// The Git identity a board needs before it can start, and which half of it the
// machine cannot supply on its own.
//
// Git requires both a name and an address to write a commit. Neither has to be
// configured: with `user.name` unset Git derives a name from the operating-system
// account — the full name on macOS, the account description on Windows — and
// commits happily. But that derivation is empty on some systems; a Linux account
// with no GECOS full name is one, and the ubuntu-24.04 CI runner is exactly that.
// There `git commit-tree` with `user.email` set and `user.name` unset fails with
// "fatal: empty ident name (for <you@example.com>) not allowed". So whether a
// name is needed is a fact about the machine, not about Git, and this module
// asks rather than assumes: `git var GIT_AUTHOR_IDENT` is Git resolving the very
// identity it would sign a commit with, configuration and derivation together,
// and it fails when it cannot. It is run with an address supplied, so that what
// it answers is about the name alone.
//
// The address is not symmetrical with the name. `workbook serve` reads it with
// `git config --get user.email` — that is the actor it records a change against —
// so a configured address is required even on a host where Git would invent one
// for a commit. (Git's own fallback is a hostname-derived address, and it refuses
// that unless the hostname is fully qualified; even where it does not refuse,
// `git config --get user.email` still answers nothing.) So an unset `user.email`
// is always missing, while an unset `user.name` is missing only when `git var`
// reports Git cannot build an identity.
//
// What `workbook serve` says when the address is missing is Git's own "git config
// --get user.email failed: exit status 1", which is true and tells nobody what to
// do. A fresh Windows install of Git has no identity at all, so this is the first
// thing a new user meets. Asking before the server is spawned turns that into a
// form — one that asks for what Git reports it cannot supply, usually just the
// address and sometimes the name too — and setting it with `git config` means the
// identity Workbook records is the same one the user's commits carry.

const { execFile } = require('node:child_process')

const GIT_TIMEOUT_MS = 5000

// A name has no real limit in Git; this only keeps a pasted paragraph out of
// every commit. An address past 254 characters cannot be delivered anyway.
const MAX_NAME = 200
const MAX_EMAIL = 254

const SCOPES = ['global', 'local']

// What to ask for when nothing is known about the checkout. The address is the
// half Git never supplies for `workbook serve`, so it is the standing default.
const DEFAULT_NEEDS = { email: true, name: false }

// `env` is for the tests, which pin the handful of variables Git's own fallbacks
// read so an answer does not depend on the account running the suite. The main
// process passes nothing and Git sees the environment Workbench was launched in.
function defaultRunner (cwd, args, env) {
  return new Promise((resolve, reject) => {
    execFile('git', args, { cwd, timeout: GIT_TIMEOUT_MS, env: env ?? process.env }, (error, stdout, stderr) => {
      if (error) {
        error.stderr = stderr
        reject(error)
      } else {
        resolve(stdout)
      }
    })
  })
}

/**
 * One key as Git resolves it for this checkout, or null when it is unset.
 *
 * `git config --get` exits 1 for a key that is simply not there, which is the
 * answer this module exists to act on. Any other failure is a real one and is
 * thrown: a repository Git cannot read is not a missing identity, and a form
 * would not fix it.
 */
async function readKey (run, repoPath, key, env) {
  try {
    const value = (await run(repoPath, ['config', '--get', key], env)).trim()
    return value || null
  } catch (error) {
    if (error.code === 1) return null
    throw error
  }
}

// An address for the name probe below to wear, so that the probe can only ever
// fail over the name. `.invalid` is reserved by RFC 2606 and can never be a real
// domain; nothing is committed with it and it is never written to a config.
const PROBE_EMAIL = 'probe@workbench.invalid'

/**
 * Whether Git can name an author in this checkout at all.
 *
 * `git var GIT_AUTHOR_IDENT` prints the identity Git would stamp on a commit and
 * exits non-zero when it cannot assemble one, which is the question worth asking:
 * it covers the name Git derives from the account without this module having to
 * guess what that account holds on this operating system.
 *
 * It answers about the whole identity, though, and only the name is being asked
 * about here — on a checkout with no `user.email` it would fail over the missing
 * address and a caller reading that as "no name" would demand a name nobody
 * needs. So the probe is run with GIT_AUTHOR_EMAIL set, which Git prefers over
 * the configuration and over its own hostname guess: the address is then never
 * the reason it fails, and a failure is the empty ident name and nothing else.
 *
 * Every failure reads as "cannot", including one that is not about the identity.
 * That is safe because the caller reads `user.email` and `user.name` through
 * readKey in the same breath, and a repository Git cannot read fails there, as a
 * thrown error rather than as a form.
 */
async function canNameAnAuthor (run, repoPath, env) {
  try {
    await run(repoPath, ['var', 'GIT_AUTHOR_IDENT'],
      { ...(env ?? process.env), GIT_AUTHOR_EMAIL: PROBE_EMAIL })
    return true
  } catch {
    return false
  }
}

/**
 * The identity Git would use in this checkout, and what it cannot supply.
 *
 * `needs.email` is set whenever `user.email` is unset, because that key is what
 * `workbook serve` reads and Git's own fallback does not answer it. `needs.name`
 * is set only when Git could not build an identity *and* `user.name` is unset —
 * on a machine whose account derivation works, a name is a courtesy rather than a
 * requirement. Complete means neither is needed.
 *
 * `name` and `email` are returned whether or not they are needed, so the form can
 * show what is already configured rather than ask for it twice.
 */
async function read (repoPath, { run = defaultRunner, env } = {}) {
  const [name, email, canName] = await Promise.all([
    readKey(run, repoPath, 'user.name', env),
    readKey(run, repoPath, 'user.email', env),
    canNameAnAuthor(run, repoPath, env)
  ])
  const needs = { email: !email, name: !canName && !name }
  return { name, email, complete: !needs.email && !needs.name, needs }
}

/**
 * Check what was typed, and hand back the trimmed values with any complaint.
 *
 * Which fields are required is `needs`, as `read` reported it: the form asks for
 * what Git cannot supply and no more, and this holds the answer to the same rule.
 * A field that was filled in anyway is still checked, because a bad address
 * written over a good one would break a checkout that worked.
 *
 * Deliberately loose about the address: one @ with something either side is
 * all Git needs, and a stricter rule would refuse real addresses. What it does
 * refuse is what Git would mangle — Git strips < and > and newlines from an
 * identity — and a leading dash, which `git config` would read as an option.
 */
function validate ({ name, email } = {}, needs = DEFAULT_NEEDS) {
  const trimmedName = typeof name === 'string' ? name.trim() : ''
  const trimmedEmail = typeof email === 'string' ? email.trim() : ''
  const errors = {}

  if (needs.name && !trimmedName) {
    errors.name = 'Enter a name.'
  } else if (trimmedName.length > MAX_NAME) {
    errors.name = `Keep the name under ${MAX_NAME} characters.`
  } else if (trimmedName && (/[<>\r\n]/.test(trimmedName) || trimmedName.startsWith('-'))) {
    errors.name = 'A name cannot contain < or >, a line break, or start with a dash.'
  }

  if (needs.email && !trimmedEmail) {
    errors.email = 'Enter an email address.'
  } else if (trimmedEmail.length > MAX_EMAIL) {
    errors.email = `Keep the address under ${MAX_EMAIL} characters.`
  } else if (trimmedEmail && (!/^[^\s@<>]+@[^\s@<>]+$/.test(trimmedEmail) || trimmedEmail.startsWith('-'))) {
    errors.email = 'Enter an address like you@example.com.'
  }

  return { ok: Object.keys(errors).length === 0, name: trimmedName, email: trimmedEmail, errors }
}

/**
 * Set the identity with `git config`, globally or for this checkout alone.
 *
 * Validated again here rather than trusted from the page: the renderer is the
 * side that can be wrong, and it is checked against what Git says is missing now
 * rather than against what was missing when the form was drawn. Only a field the
 * user filled is written — a blank one is left to Git, because an empty
 * `user.name` is not the same as an unset one (Git refuses to commit with one)
 * and writing it would break the checkout this form exists to make usable.
 *
 * Returns the identity as Git now resolves it rather than what was written,
 * because a global value is still shadowed by a local one the checkout already
 * carries, and because whether anything is still missing is the caller's answer.
 */
async function write (repoPath, { name, email, scope } = {}, { run = defaultRunner, env } = {}) {
  if (!SCOPES.includes(scope)) throw new Error(`unknown identity scope: ${scope}`)

  const before = await read(repoPath, { run, env })
  const checked = validate({ name, email }, before.needs)
  if (!checked.ok) throw new Error(Object.values(checked.errors).join(' '))

  const where = scope === 'global' ? '--global' : '--local'
  if (checked.name) await run(repoPath, ['config', where, 'user.name', checked.name], env)
  if (checked.email) await run(repoPath, ['config', where, 'user.email', checked.email], env)
  return read(repoPath, { run, env })
}

module.exports = { read, validate, write, SCOPES, MAX_NAME, MAX_EMAIL, DEFAULT_NEEDS }
