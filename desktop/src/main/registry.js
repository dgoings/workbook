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
  sidebarCollapsed: false,
  pathNoticeShown: false,
  pendingPathNotice: null
}

class Registry {
  /** @param {string} directory Electron's userData path */
  constructor (directory) {
    this.file = path.join(directory, 'registry.json')
    this.state = structuredClone(EMPTY)
    /** The tail of the save queue; see save(). @type {Promise<void>} */
    this.saving = Promise.resolve()
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

  /**
   * Persist the registry, one save at a time.
   *
   * The write itself is atomic, but two of them overlapping are not: both use
   * the one temp path, so the first rename takes the file the second is still
   * filling. That loses the second save to ENOENT, and in the worse
   * interleaving renames a half-written file into place, which the next load()
   * can only quarantine — the project list looks gone until a rescan. Saves are
   * therefore queued rather than run as they arrive. The queue lives here, not
   * at the call site, because every caller needs it: the sidebar shortcut is
   * only the first one a user can fire twice in a moment.
   */
  async save () {
    const done = this.saving.then(() => this.#write())
    // The next save waits on a promise that always settles. Waiting on this one
    // would hand it a failed save's rejection and break the queue for good.
    this.saving = done.catch(() => {})
    return done
  }

  /** The actual write. Only save() calls it, and only one call at a time. */
  async #write () {
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

  /** 'system' | 'light' | 'dark' */
  get theme () {
    return this.state.theme ?? 'system'
  }

  async setTheme (theme) {
    this.state.theme = theme
    await this.save()
  }

  /**
   * Whether the sidebar is showing as a narrow rail.
   *
   * Read strictly: a registry written by an older build has no such field at
   * all, and a missing answer means the sidebar is expanded.
   */
  get sidebarCollapsed () {
    return this.state.sidebarCollapsed === true
  }

  /**
   * Store the collapsed state, or leave it exactly as it was.
   *
   * The caller moves native board views to match this value, so a rejected
   * write that still changed it in memory would be worse than no write at all:
   * the next layout would position every board for a width the sidebar is not
   * drawn at, and the sidebar would end up underneath a board. Rolling back
   * keeps the stored answer and the painted one the same answer.
   */
  async setSidebarCollapsed (collapsed) {
    const previous = this.state.sidebarCollapsed
    this.state.sidebarCollapsed = collapsed === true
    try {
      await this.save()
    } catch (error) {
      this.state.sidebarCollapsed = previous
      throw error
    }
  }

  /**
   * Whether the user has already been told the CLI is on their PATH.
   *
   * Read strictly, like sidebarCollapsed: a registry written by a build from
   * before the PATH install has no such field, and a missing answer means the
   * notice has not been shown. The fact lives here rather than in the
   * renderer's localStorage — where the project-key caution's dismissal lives —
   * because it is the app's own fact about something it did to the machine
   * once, and it has to survive a cleared renderer storage.
   */
  get pathNoticeShown () {
    return this.state.pathNoticeShown === true
  }

  /**
   * The directory a notice is still owed for, or null.
   *
   * This is the part that makes the notice survive the launch that earned it.
   * The install is what knows PATH changed, and it finishes whenever it
   * finishes; the page that has to say so may never get the chance — the user
   * quits while it is still loading, or the renderer throws on the way up. A
   * fact about the conversation ("we have said it") cannot recover from that,
   * because the next launch changes nothing and so has nothing to report. A
   * fact about the machine ("this directory was added and nobody has been told
   * yet") can: it sits here until some launch's page asks for it.
   *
   * Read defensively — anything but a non-empty string means nothing is owed.
   */
  get pendingPathNotice () {
    const pending = this.state.pendingPathNotice
    return typeof pending === 'string' && pending !== '' ? pending : null
  }

  /**
   * Arm the notice for `directory`, unless it has already been said.
   *
   * The "once ever" rule lives here rather than at the call site so that no
   * caller can break it: an app update that copies a fresh binary and adds its
   * directory again must not re-raise a notice the user has already read and
   * dismissed. Storing the same pending directory twice is a harmless no-op and
   * is skipped, so a second launch that is still waiting to say it does not
   * rewrite the registry for nothing.
   */
  async setPendingPathNotice (directory) {
    if (this.pathNoticeShown) return
    if (this.state.pendingPathNotice === directory) return
    this.state.pendingPathNotice = directory
    await this.save()
  }

  /**
   * Record that the notice has been handed to the renderer, and disarm it.
   *
   * Both halves in one save: "said it" and "nothing is owed" are two spellings
   * of the same fact, and a write that landed one without the other would
   * either say it twice or lose it. One-way and argumentless — there is no
   * reason to un-say it, and the main process marks it as it answers
   * `path:notice` rather than waiting for the dismissal, so a user who quits
   * without clicking the dismiss button has still been told. A rejected write
   * is not swallowed; the caller decides.
   */
  async setPathNoticeShown () {
    this.state.pathNoticeShown = true
    this.state.pendingPathNotice = null
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
