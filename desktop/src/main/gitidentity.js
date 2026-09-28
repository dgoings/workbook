'use strict'

// The Git identity a board needs before it can start.
//
// `workbook serve` records every change against the checkout's user.email and
// will not start without one. What it says when there is none is Git's own
// "git config --get user.email failed: exit status 1", which is true and tells
// nobody what to do. A fresh Windows install of Git has no identity at all, so
// this is the first thing a new user meets. Asking before the server is spawned
// turns that into a form, and setting it with `git config` means the identity
// Workbook records is the same one the user's commits carry.

const { execFile } = require('node:child_process')

const GIT_TIMEOUT_MS = 5000

// A name has no real limit in Git; this only keeps a pasted paragraph out of
// every commit. An address past 254 characters cannot be delivered anyway.
const MAX_NAME = 200
const MAX_EMAIL = 254

const SCOPES = ['global', 'local']

function defaultRunner (cwd, args) {
  return new Promise((resolve, reject) => {
    execFile('git', args, { cwd, timeout: GIT_TIMEOUT_MS }, (error, stdout, stderr) => {
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
async function readKey (run, repoPath, key) {
  try {
    const value = (await run(repoPath, ['config', '--get', key])).trim()
    return value || null
  } catch (error) {
    if (error.code === 1) return null
    throw error
  }
}

/**
 * The identity Git would use in this checkout: local, then global, then system.
 *
 * Both halves are asked for. Workbook's actor is the email alone, but its
 * commits carry a name as well, and asking for one while leaving the other to
 * fail on the first write would be the same surprise one step later.
 */
async function read (repoPath, { run = defaultRunner } = {}) {
  const [name, email] = await Promise.all([
    readKey(run, repoPath, 'user.name'),
    readKey(run, repoPath, 'user.email')
  ])
  return { name, email, complete: Boolean(name && email) }
}

/**
 * Check what was typed, and hand back the trimmed values with any complaint.
 *
 * Deliberately loose about the address: one @ with something either side is
 * all Git needs, and a stricter rule would refuse real addresses. What it does
 * refuse is what Git would mangle — Git strips < and > and newlines from an
 * identity — and a leading dash, which `git config` would read as an option.
 */
function validate ({ name, email } = {}) {
  const trimmedName = typeof name === 'string' ? name.trim() : ''
  const trimmedEmail = typeof email === 'string' ? email.trim() : ''
  const errors = {}

  if (!trimmedName) {
    errors.name = 'Enter a name.'
  } else if (trimmedName.length > MAX_NAME) {
    errors.name = `Keep the name under ${MAX_NAME} characters.`
  } else if (/[<>\r\n]/.test(trimmedName) || trimmedName.startsWith('-')) {
    errors.name = 'A name cannot contain < or >, a line break, or start with a dash.'
  }

  if (!trimmedEmail) {
    errors.email = 'Enter an email address.'
  } else if (trimmedEmail.length > MAX_EMAIL) {
    errors.email = `Keep the address under ${MAX_EMAIL} characters.`
  } else if (!/^[^\s@<>]+@[^\s@<>]+$/.test(trimmedEmail) || trimmedEmail.startsWith('-')) {
    errors.email = 'Enter an address like you@example.com.'
  }

  return { ok: Object.keys(errors).length === 0, name: trimmedName, email: trimmedEmail, errors }
}

/**
 * Set the identity with `git config`, globally or for this checkout alone.
 *
 * Validated again here rather than trusted from the page: the renderer is the
 * side that can be wrong. Returns the identity as Git now resolves it rather
 * than what was written, because a global value is still shadowed by a local
 * one the checkout already carries, and the caller has to know which it got.
 */
async function write (repoPath, { name, email, scope } = {}, { run = defaultRunner } = {}) {
  const checked = validate({ name, email })
  if (!checked.ok) throw new Error(Object.values(checked.errors).join(' '))
  if (!SCOPES.includes(scope)) throw new Error(`unknown identity scope: ${scope}`)

  const where = scope === 'global' ? '--global' : '--local'
  await run(repoPath, ['config', where, 'user.name', checked.name])
  await run(repoPath, ['config', where, 'user.email', checked.email])
  return read(repoPath, { run })
}

module.exports = { read, validate, write, SCOPES, MAX_NAME, MAX_EMAIL }
