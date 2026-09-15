'use strict'

// The list of imported projects, persisted between runs.
//
// This holds only what Workbench needs to find a repository again: paths and
// display names. Task data is never copied here — it lives in each repository's
// refs/workbook/*, which is the whole point of the storage model. A registry
// entry going stale is recoverable by rescanning; a registry entry is never
// the source of truth for anything.

const fs = require('node:fs/promises')
const path = require('node:path')

const EMPTY = {
  version: 1,
  scanRoots: [],
  projects: [],
  theme: 'system',
  // The email Workbook assigns against for "me". Seeded from git's own
  // user.email on first run, because that is what `--assign self` already
  // records; it is a setting only so that a machine shared by two people, or a
  // person with two addresses, can say which one this is.
  defaultAssignee: null,
  // Corrections to what the commit history implies: identities that belong to
  // one person, and the names to show them under. Only what the user changed is
  // stored — the directory itself is derived on demand.
  people: { merges: [], names: {} }
}

class Registry {
  /** @param {string} directory Electron's userData path */
  constructor (directory) {
    this.file = path.join(directory, 'registry.json')
    this.state = structuredClone(EMPTY)
  }

  async load () {
    let raw
    try {
      raw = await fs.readFile(this.file, 'utf8')
    } catch {
      this.state = structuredClone(EMPTY) // First run.
      return this.state
    }

    try {
      const parsed = JSON.parse(raw)
      if (!Array.isArray(parsed.projects)) throw new Error('no projects array')
      this.state = { ...structuredClone(EMPTY), ...parsed }
      return this.state
    } catch (error) {
      // A registry that exists but will not parse is not the same as no
      // registry. Starting empty and then saving over it would destroy the
      // user's project list for good, so the unreadable file is kept and the
      // next save writes beside it rather than on top of it.
      const quarantine = `${this.file}.corrupt-${Date.now()}`
      try {
        await fs.rename(this.file, quarantine)
        console.error(`workbench: registry.json could not be parsed (${error.message}); ` +
          `kept a copy at ${quarantine}`)
      } catch {
        console.error(`workbench: registry.json could not be parsed (${error.message}) ` +
          'and could not be set aside')
      }
      this.state = structuredClone(EMPTY)
      return this.state
    }
  }

  async save () {
    await fs.mkdir(path.dirname(this.file), { recursive: true })
    const temporary = `${this.file}.tmp`
    await fs.writeFile(temporary, JSON.stringify(this.state, null, 2))
    await fs.rename(temporary, this.file) // Atomic: never leave a half-written registry.
  }

  get projects () {
    return this.state.projects
  }

  get scanRoots () {
    return this.state.scanRoots
  }

  get defaultAssignee () {
    return this.state.defaultAssignee ?? null
  }

  async setDefaultAssignee (email) {
    this.state.defaultAssignee = email ? String(email).trim().toLowerCase() : null
    await this.save()
  }

  get peopleMapping () {
    return { merges: [], names: {}, ...(this.state.people ?? {}) }
  }

  /** Record that these addresses are one person. */
  async mergePeople (emails) {
    const group = emails.map((email) => String(email).trim().toLowerCase()).filter(Boolean)
    if (group.length < 2) return
    const mapping = this.peopleMapping
    // Fold into any existing group that already shares an address, so merging
    // A+B and then B+C leaves one person rather than two overlapping ones.
    const overlapping = mapping.merges.filter((existing) =>
      existing.some((email) => group.includes(email)))
    const rest = mapping.merges.filter((existing) => !overlapping.includes(existing))
    const combined = [...new Set([...group, ...overlapping.flat()])]
    this.state.people = { ...mapping, merges: [...rest, combined] }
    await this.save()
  }

  async splitPerson (email) {
    const needle = String(email).trim().toLowerCase()
    const mapping = this.peopleMapping
    this.state.people = {
      ...mapping,
      merges: mapping.merges
        .map((group) => group.filter((member) => member !== needle))
        .filter((group) => group.length > 1)
    }
    await this.save()
  }

  async renamePerson (id, displayName) {
    const mapping = this.peopleMapping
    const names = { ...mapping.names }
    if (displayName) names[id] = displayName
    else delete names[id]
    this.state.people = { ...mapping, names }
    await this.save()
  }

  /** 'system' | 'light' | 'dark' */
  get theme () {
    return this.state.theme ?? 'system'
  }

  async setTheme (theme) {
    this.state.theme = theme
    await this.save()
  }

  find (projectId) {
    return this.state.projects.find((project) => project.id === projectId) ?? null
  }

  async rememberScanRoot (root) {
    if (!this.state.scanRoots.includes(root)) {
      this.state.scanRoots.unshift(root)
      this.state.scanRoots = this.state.scanRoots.slice(0, 10)
      await this.save()
    }
  }

  /**
   * Add or update a project. Keyed by projectId, which Workbook mints and never
   * changes, so a repository that moved on disk updates in place rather than
   * appearing twice.
   */
  async upsert (project) {
    const index = this.state.projects.findIndex((existing) => existing.id === project.id)
    if (index === -1) {
      this.state.projects.push(project)
    } else {
      this.state.projects[index] = { ...this.state.projects[index], ...project }
    }
    await this.save()
    return project
  }

  async remove (projectId) {
    this.state.projects = this.state.projects.filter((project) => project.id !== projectId)
    await this.save()
  }
}

module.exports = { Registry }
