'use strict'

// One `workbook serve` process per imported project, supervised.
//
// Why a child process per repository rather than one server for all of them:
// `serve` binds itself to the repository at its working directory, and the
// board's same-origin guard pins the Host header to the address that listener
// actually bound. Running the real thing per project and pointing a view at it
// satisfies both without a proxy in the middle rewriting either.
//
// The board also sends `frame-ancestors 'none'`, so these addresses cannot be
// put in an iframe. Each one is loaded in its own WebContentsView instead,
// which is a top-level contents and never triggers that check.

const { spawn, execFileSync } = require('node:child_process')
const { EventEmitter } = require('node:events')
const fs = require('node:fs')
const path = require('node:path')
const { resolveBinary } = require('./workbook')

// serve prints `Workbook board: http://127.0.0.1:7331` to stderr once bound.
// When 7331 is taken it binds a free port and prints the one it chose, so the
// banner — not the default — is the only reliable source for the address.
const BANNER = /Workbook board:\s*(https?:\/\/\S+)/

const START_TIMEOUT_MS = 15000

/**
 * Kill board servers left behind by a previous run.
 *
 * No in-process handler can cover every exit: Force Quit is SIGKILL, and a
 * crash is worse, so `before-quit` never runs and the children outlive the app
 * holding their ports. The only thing that survives that is a record on disk,
 * checked at startup.
 *
 * A recorded pid is not trusted on its own — pids are reused, and killing an
 * unrelated process would be far worse than leaving a stale server running. The
 * process is killed only if it is still the command we started.
 */
function reapPreviousRun (recordPath) {
  let recorded
  try {
    recorded = JSON.parse(fs.readFileSync(recordPath, 'utf8'))
  } catch {
    return [] // No previous run, or an unreadable record: nothing to reap.
  }
  if (!Array.isArray(recorded)) return []

  const reaped = []
  for (const entry of recorded) {
    if (!entry || !Number.isInteger(entry.pid)) continue
    let command = ''
    try {
      command = execFileSync('ps', ['-p', String(entry.pid), '-o', 'command='],
        { encoding: 'utf8' }).trim()
    } catch {
      continue // Not running any more, which is the outcome we wanted.
    }
    // It must still be the board server we started, from the binary we started
    // it with, before it is worth signalling.
    if (!command.includes('serve') || !command.includes(entry.binary ?? '\u0000')) continue
    try {
      process.kill(entry.pid, 'SIGTERM')
      reaped.push(entry.pid)
    } catch {
      // Gone between the check and the signal, or not ours to kill.
    }
  }
  try {
    fs.unlinkSync(recordPath)
  } catch {
    // Already gone.
  }
  return reaped
}

class Supervisor extends EventEmitter {
  constructor (userDataPath) {
    super()
    // Where the pids of running board servers are recorded, so a run that ends
    // without warning can be cleaned up by the next one.
    this.recordPath = userDataPath
      ? path.join(userDataPath, 'running-boards.json')
      : null
    /** @type {Map<string, {child: import('node:child_process').ChildProcess, url: string|null, status: string, error: string|null, log: string[]}>} */
    this.processes = new Map()
    this.stopping = false
  }

  /** Kill anything a previous run left behind. Returns the pids signalled. */
  reapOrphans () {
    return this.recordPath ? reapPreviousRun(this.recordPath) : []
  }

  /** Persist the pids currently running, for the next run to clean up. */
  recordRunning () {
    if (!this.recordPath) return
    const entries = [...this.processes.values()]
      .filter((entry) => entry.child.pid && !entry.child.killed)
      .map((entry) => ({ pid: entry.child.pid, binary: entry.binary }))
    try {
      if (entries.length === 0) fs.rmSync(this.recordPath, { force: true })
      else fs.writeFileSync(this.recordPath, JSON.stringify(entries))
    } catch {
      // A record that cannot be written only costs the next run its cleanup.
    }
  }

  get (projectId) {
    return this.processes.get(projectId) ?? null
  }

  status (projectId) {
    const entry = this.processes.get(projectId)
    if (!entry) return { status: 'stopped', url: null, error: null }
    return { status: entry.status, url: entry.url, error: entry.error }
  }

  /**
   * Start a board for one project, or return the address of the running one.
   * @param {{id: string, path: string}} project
   * @param {{binary?: string}} [options]
   * @returns {Promise<string>} the bound board URL
   */
  async start (project, options = {}) {
    const existing = this.processes.get(project.id)
    if (existing && existing.status === 'running' && existing.url) {
      return existing.url
    }
    if (existing && existing.status === 'starting') {
      return existing.pending
    }

    const binary = await resolveBinary(options.binary)
    const child = spawn(binary, ['serve'], { cwd: project.path, env: process.env })

    const entry = {
      child, binary, url: null, status: 'starting', error: null, log: [], pending: null
    }
    this.processes.set(project.id, entry)
    this.recordRunning()

    entry.pending = new Promise((resolve, reject) => {
      let settled = false

      const timer = setTimeout(() => {
        if (settled) return
        settled = true
        entry.status = 'failed'
        entry.error = 'timed out waiting for the board to bind'
        child.kill()
        reject(new Error(entry.error))
      }, START_TIMEOUT_MS)

      const onText = (text) => {
        entry.log.push(text)
        if (entry.log.length > 200) entry.log.shift()
        const match = text.match(BANNER)
        if (match && !settled) {
          settled = true
          clearTimeout(timer)
          entry.url = match[1].trim()
          entry.status = 'running'
          this.emit('started', { projectId: project.id, url: entry.url })
          resolve(entry.url)
        }
      }

      child.stderr.on('data', (chunk) => onText(chunk.toString()))
      child.stdout.on('data', (chunk) => onText(chunk.toString()))

      child.on('error', (error) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        entry.status = 'failed'
        entry.error = error.message
        reject(error)
      })

      child.on('close', (code) => {
        clearTimeout(timer)
        const wasRunning = entry.status === 'running'
        entry.status = this.stopping ? 'stopped' : 'exited'
        entry.url = null
        if (!this.stopping && code !== 0) {
          entry.error = entry.log.join('').trim().split('\n').slice(-3).join('\n') ||
            `serve exited with code ${code}`
        }
        this.emit('exited', { projectId: project.id, code, wasRunning })
        if (!settled) {
          settled = true
          reject(new Error(entry.error ?? `serve exited with code ${code}`))
        }
      })
    })

    return entry.pending
  }

  /** Stop one project's board. */
  stop (projectId) {
    const entry = this.processes.get(projectId)
    if (!entry) return
    entry.status = 'stopped'
    this.terminate(entry.child)
    this.processes.delete(projectId)
    this.recordRunning()
  }

  /**
   * Ask a child to stop, and insist if it does not.
   *
   * SIGTERM lets the server close its listener and remove its watcher socket.
   * A child that ignores it would otherwise outlive the app holding a port, so
   * the signal is escalated rather than hoped about.
   */
  terminate (child) {
    if (!child.pid || child.killed) return
    try {
      child.kill('SIGTERM')
    } catch {
      return
    }
    setTimeout(() => {
      try {
        if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL')
      } catch {
        // Already reaped.
      }
    }, 2000).unref?.()
  }

  /**
   * Stop every board. Called on quit: these are real child processes holding
   * listeners, and leaking them would leave ports bound after the app closes.
   */
  stopAll () {
    this.stopping = true
    for (const [, entry] of this.processes) {
      this.terminate(entry.child)
    }
    this.processes.clear()
    this.recordRunning()
  }
}

module.exports = { Supervisor }
