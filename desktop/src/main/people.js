'use strict'

// Who works on these repositories, assembled from the commit history that is
// already on disk.
//
// Workbook assigns against an email address: `--assign self` records the
// checkout's user.email, and `--assign someone@example.com` records that. So an
// assignee picker needs a list of addresses, and a human needs those addresses
// to have names attached. Both are in the commit history of the repositories
// already imported, which means the directory can be built rather than typed.
//
// One person is usually several addresses — a work one, a personal one, a
// GitHub noreply — and the same address is often several spellings of a name.
// Grouping those is guesswork, so this module guesses only where the evidence
// is unambiguous and leaves the rest to an explicit mapping the user controls.

const EMAIL_PATTERN = /^[^@\s]+@[^@\s]+$/

/** GitHub hands out noreply addresses of the form 12345+login@users.noreply.github.com. */
const GITHUB_NOREPLY = /^(?:\d+\+)?([^@]+)@users\.noreply\.github\.com$/i

// Addresses that name a tool rather than a person, or a fixture rather than a
// human. They are still listed, just not proposed as assignees.
const BOT_PATTERN = /(\[bot\]|^bot@|noreply@(?!users\.noreply\.github)|actions@github\.com|^a@example\.com$)/i

function normalizeName (name) {
  return String(name ?? '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, ' ')
    .trim()
}

// The same name with every separator removed. GitHub logins are usually a
// person's name run together — "tylerkilgore" for "Tyler Kilgore" — so this is
// what makes a noreply address match the human it belongs to.
function squashName (name) {
  return normalizeName(name).replace(/ /g, '')
}

/** The login half of a GitHub noreply address, which is a real identity hint. */
function githubLogin (email) {
  const match = GITHUB_NOREPLY.exec(email)
  return match ? match[1].toLowerCase() : null
}

function isValidEmail (email) {
  return EMAIL_PATTERN.test(String(email ?? '').trim())
}

/**
 * Collapse a set of email identities into people.
 *
 * Two identities are merged only on evidence that does not require a judgement
 * call: the same display name spelled the same way, or a GitHub noreply address
 * whose login matches another identity's name. Anything less certain is left
 * separate, because two rows for one person is a cosmetic problem and one row
 * for two people is a wrong assignment.
 *
 * `merges` from the user's own mapping are applied first and are absolute:
 * an explicit grouping is evidence this function cannot derive.
 *
 * @param {Array<{email: string, name: string, commits: number, repo: string}>} identities
 * @param {{merges?: string[][], names?: Record<string,string>}} [mapping]
 */
function groupIdentities (identities, mapping = {}) {
  /** @type {Map<string, {email: string, names: Map<string, number>, commits: number, repos: Set<string>}>} */
  const byEmail = new Map()

  for (const identity of identities) {
    const email = String(identity.email ?? '').trim().toLowerCase()
    if (!isValidEmail(email)) continue
    const entry = byEmail.get(email) ??
      { email, names: new Map(), commits: 0, repos: new Set() }
    entry.commits += identity.commits ?? 1
    const name = String(identity.name ?? '').trim()
    if (name) entry.names.set(name, (entry.names.get(name) ?? 0) + (identity.commits ?? 1))
    if (identity.repo) entry.repos.add(identity.repo)
    byEmail.set(email, entry)
  }

  // Union-find over email addresses. Small enough that the naive form is the
  // readable one.
  const parent = new Map()
  const find = (email) => {
    let root = email
    while (parent.get(root) && parent.get(root) !== root) root = parent.get(root)
    return root
  }
  const union = (a, b) => {
    const rootA = find(a)
    const rootB = find(b)
    if (rootA !== rootB) parent.set(rootB, rootA)
  }
  for (const email of byEmail.keys()) parent.set(email, email)

  // 1. The user's explicit mapping, which outranks every heuristic below.
  for (const group of mapping.merges ?? []) {
    const known = group.map((e) => String(e).toLowerCase()).filter((e) => byEmail.has(e))
    for (let i = 1; i < known.length; i += 1) union(known[0], known[i])
  }

  // 2. The same display name, compared with separators removed so that
  //    "Tyler Kilgore" and "TylerKilgore" are one person rather than two.
  const bySquashedName = new Map()
  for (const [email, entry] of byEmail) {
    const dominant = [...entry.names.entries()].sort((a, b) => b[1] - a[1])[0]?.[0]
    const key = squashName(dominant)
    if (!key || key.length < 4) continue // "a" or "js" is not evidence of anything
    const existing = bySquashedName.get(key)
    if (existing) union(existing, email)
    else bySquashedName.set(key, email)
  }

  // 3. A GitHub noreply address whose login matches somebody's name. The login
  //    half is chosen by the person it belongs to, so it is a stronger hint
  //    than a coincidence of spelling.
  for (const [email] of byEmail) {
    const login = githubLogin(email)
    if (!login) continue
    const match = bySquashedName.get(squashName(login))
    if (match) union(match, email)
  }

  /** @type {Map<string, {emails: string[], names: Map<string, number>, commits: number, repos: Set<string>}>} */
  const groups = new Map()
  for (const [email, entry] of byEmail) {
    const root = find(email)
    const group = groups.get(root) ??
      { emails: [], names: new Map(), commits: 0, repos: new Set() }
    group.emails.push(email)
    group.commits += entry.commits
    for (const [name, count] of entry.names) {
      group.names.set(name, (group.names.get(name) ?? 0) + count)
    }
    for (const repo of entry.repos) group.repos.add(repo)
    groups.set(root, group)
  }

  const people = [...groups.entries()].map(([id, group]) => {
    const dominantName = [...group.names.entries()]
      .sort((a, b) => b[1] - a[1])[0]?.[0] ?? id
    return {
      id,
      // A name the user set always wins over the one git happened to record.
      displayName: mapping.names?.[id] ?? dominantName,
      // The primary address first: it is the one an assignment should use.
      emails: group.emails.sort((a, b) => (a === id ? -1 : b === id ? 1 : a.localeCompare(b))),
      commits: group.commits,
      repos: [...group.repos].sort(),
      bot: group.emails.every((email) => BOT_PATTERN.test(email))
    }
  })

  people.sort((a, b) => b.commits - a.commits || a.displayName.localeCompare(b.displayName))
  return people
}

/**
 * Build the directory from what a scan already read out of each repository.
 *
 * @param {Array<{path: string, name: string, allAuthors?: Array<{email: string, name: string, commits: number}>}>} repositories
 * @param {{merges?: string[][], names?: Record<string,string>}} [mapping]
 */
function buildDirectory (repositories, mapping = {}) {
  const identities = []
  for (const repository of repositories) {
    for (const author of repository.allAuthors ?? []) {
      identities.push({ ...author, repo: repository.name ?? repository.path })
    }
  }
  return groupIdentities(identities, mapping)
}

/** The person an email belongs to, for labelling an assignment. */
function findPerson (people, email) {
  const needle = String(email ?? '').trim().toLowerCase()
  return people.find((person) => person.emails.includes(needle)) ?? null
}

module.exports = { buildDirectory, groupIdentities, findPerson, isValidEmail, githubLogin }
