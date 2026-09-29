'use strict'

// The Git identity a board needs before it can start.
//
// What a board needs is an email address, and nothing else. `workbook serve`
// records every change against the checkout's user.email — that is the actor it
// writes into a task's history — and will not start without one. Each change is
// then written with `git commit-tree`, which needs a committer identity, and the
// email is the half of one Git cannot invent. The name it can: with user.email
// set and user.name unset, `git commit-tree` succeeds and takes the name from
// the account. So a missing name is not what stops a board, and this form does
// not demand one; it offers the field, because a name Git guessed from an
// account is worse than one the user chose, and it is recorded when given.
//
// What `workbook serve` says when the email is missing is Git's own "git config
// --get user.email failed: exit status 1", which is true and tells nobody what
// to do. A fresh Windows install of Git has no identity at all, so this is the
// first thing a new user meets. Asking before the server is spawned turns that
// into a form, and setting it with `git config` means the identity Workbook
// records is the same one the user's commits carry.

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
 * Complete means the email is set, which is everything a board needs. The name
 * is read so the form can show one that is already configured rather than
 * asking for it twice, and its absence never makes an identity incomplete.
 */
async function read (repoPath, { run = defaultRunner } = {}) {
  const [name, email] = await Promise.all([
    readKey(run, repoPath, 'user.name'),
    readKey(run, repoPath, 'user.email')
  ])
  return { name, email, complete: Boolean(email) }
}

/**
 * Check what was typed, and hand back the trimmed values with any complaint.
 *
 * The email is required and the name is not, because that is what a board
 * needs. A name that was typed is still held to the same rules as before.
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

  if (trimmedName.length > MAX_NAME) {
    errors.name = `Keep the name under ${MAX_NAME} characters.`
  } else if (trimmedName && (/[<>\r\n]/.test(trimmedName) || trimmedName.startsWith('-'))) {
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
  // A name that was left blank is left to Git rather than written as an empty
  // value: an empty user.name is not the same as an unset one — Git refuses to
  // commit with one — so writing it would break the checkout this form exists
  // to make usable.
  if (checked.name) await run(repoPath, ['config', where, 'user.name', checked.name])
  await run(repoPath, ['config', where, 'user.email', checked.email])
  return read(repoPath, { run })
}

module.exports = { read, validate, write, SCOPES, MAX_NAME, MAX_EMAIL }
