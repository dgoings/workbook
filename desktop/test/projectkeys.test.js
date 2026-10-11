'use strict'

// projectkeys.js keeps the key each project is labeled with — in the sidebar,
// the rail, the menu and the Next view — at the project's *current* key, the
// one `workbook key current` moves, rather than the key it was imported under.
// The CLI runner and the registry are injected, so nothing here spawns a
// process or loads Electron.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const projectkeys = require('../src/main/projectkeys')

/** What `workbook key list --json` hands back as `data`, trimmed to the parts read. */
function keyList (current, keys = [current]) {
  return {
    head: 'abc123',
    seeded: true,
    current,
    keys: keys.map((key) => ({ key, state: 'active', current: key === current, tasks: 0 }))
  }
}

describe('currentKey', () => {
  test('reads the current key from `workbook key list`', async () => {
    const calls = []
    const runJSON = async (cwd, args) => {
      calls.push([cwd, args])
      return keyList('NEW', ['OLD', 'NEW'])
    }
    assert.equal(await projectkeys.currentKey('/repo', { runJSON }), 'NEW')
    // A read: no sync flag is needed because `key list` never fetches.
    assert.deepEqual(calls, [['/repo', ['key', 'list']]])
  })

  test('refuses an answer that is not a key, rather than labeling a project with it', async () => {
    for (const current of [undefined, null, '', 'lower', 42, 'TOOLONGAKEY1']) {
      const runJSON = async () => ({ current })
      await assert.rejects(projectkeys.currentKey('/repo', { runJSON }), /current key/)
    }
    await assert.rejects(projectkeys.currentKey('/repo', { runJSON: async () => null }), /current key/)
  })
})

describe('staleKeys', () => {
  test('a project whose current key moved since it was recorded reports the new one', async () => {
    const projects = [
      { id: 'p1', key: 'OLD', path: '/one' },
      { id: 'p2', key: 'TWO', path: '/two' }
    ]
    const read = async (repoPath) => (repoPath === '/one' ? 'NEW' : 'TWO')
    assert.deepEqual(await projectkeys.staleKeys({ projects, read }), [{ id: 'p1', key: 'NEW' }])
  })

  test('a project that cannot be read keeps its last known key, and the others are still read', async () => {
    const projects = [
      { id: 'gone', key: 'GONE', path: '/moved-away' },
      { id: 'p2', key: 'OLD', path: '/two' }
    ]
    const read = async (repoPath) => {
      if (repoPath === '/moved-away') throw new Error('spawn workbook ENOENT')
      return 'NEW'
    }
    assert.deepEqual(await projectkeys.staleKeys({ projects, read }), [{ id: 'p2', key: 'NEW' }])
  })
})

describe('createKeyRefresher', () => {
  /** A registry stand-in holding projects in memory, with setKeys as registry.js has it. */
  function memoryRegistry (projects) {
    return {
      projects,
      writes: 0,
      async setKeys (changes) {
        this.writes += 1
        const applied = []
        for (const { id, key } of changes) {
          const project = this.projects.find((entry) => entry.id === id)
          if (!project || project.key === key) continue
          project.key = key
          applied.push({ id, key })
        }
        return applied
      }
    }
  }

  test('the stale key in the registry is replaced by the current one, and the shell is told', async () => {
    const registry = memoryRegistry([
      { id: 'p1', key: 'OLD', path: '/one' },
      { id: 'p2', key: 'TWO', path: '/two' }
    ])
    const announced = []
    const refresher = projectkeys.createKeyRefresher({
      projects: () => registry.projects,
      read: async (repoPath) => (repoPath === '/one' ? 'NEW' : 'TWO'),
      apply: (changes) => registry.setKeys(changes),
      onChanged: (applied) => announced.push(applied)
    })
    await refresher.refresh()
    assert.deepEqual(registry.projects.map((entry) => entry.key), ['NEW', 'TWO'])
    assert.deepEqual(announced, [[{ id: 'p1', key: 'NEW' }]])
  })

  test('nothing is written or announced when every key is already current', async () => {
    const registry = memoryRegistry([{ id: 'p1', key: 'ONE', path: '/one' }])
    let announced = 0
    const refresher = projectkeys.createKeyRefresher({
      projects: () => registry.projects,
      read: async () => 'ONE',
      apply: (changes) => registry.setKeys(changes),
      onChanged: () => { announced += 1 }
    })
    await refresher.refresh()
    assert.equal(registry.writes, 0)
    assert.equal(announced, 0)
  })

  test('a refresh can name the projects it is about', async () => {
    const registry = memoryRegistry([
      { id: 'p1', key: 'OLD', path: '/one' },
      { id: 'p2', key: 'OLD2', path: '/two' }
    ])
    const read = []
    const refresher = projectkeys.createKeyRefresher({
      projects: () => registry.projects,
      read: async (repoPath) => { read.push(repoPath); return 'NEW' },
      apply: (changes) => registry.setKeys(changes),
      onChanged: () => {}
    })
    await refresher.refresh(['p2'])
    assert.deepEqual(read, ['/two'])
    assert.deepEqual(registry.projects.map((entry) => entry.key), ['OLD', 'NEW'])
  })

  test('requests that arrive while a read is running are run once afterwards, not alongside it', async () => {
    const registry = memoryRegistry([
      { id: 'p1', key: 'ONE', path: '/one' },
      { id: 'p2', key: 'TWO', path: '/two' }
    ])
    const running = new Map()
    let most = 0
    const read = []
    let release
    const gate = new Promise((resolve) => { release = resolve })
    const refresher = projectkeys.createKeyRefresher({
      projects: () => registry.projects,
      read: async (repoPath) => {
        running.set(repoPath, (running.get(repoPath) ?? 0) + 1)
        most = Math.max(most, running.get(repoPath))
        read.push(repoPath)
        await gate
        running.set(repoPath, running.get(repoPath) - 1)
        return repoPath === '/one' ? 'ONE' : 'TWO'
      },
      apply: (changes) => registry.setKeys(changes),
      onChanged: () => {}
    })
    const first = refresher.refresh(['p1'])
    // Three more triggers while the first read is held: a board starting, the
    // window taking focus, a second board starting.
    const second = refresher.refresh(['p2'])
    const third = refresher.refresh(['p1'])
    const fourth = refresher.refresh(['p2'])
    release()
    await Promise.all([first, second, third, fourth])
    // One read for the first request, then one pass for everything that queued
    // behind it, so a project is never read twice at once and an older read
    // can never land after a newer one.
    assert.deepEqual(read, ['/one', '/one', '/two'])
    assert.equal(most, 1)
  })

  test('a project forgotten before its turn is not read', async () => {
    const registry = memoryRegistry([{ id: 'p1', key: 'ONE', path: '/one' }])
    const read = []
    const refresher = projectkeys.createKeyRefresher({
      projects: () => registry.projects,
      read: async (repoPath) => { read.push(repoPath); return 'ONE' },
      apply: (changes) => registry.setKeys(changes),
      onChanged: () => {}
    })
    await refresher.refresh(['forgotten'])
    assert.deepEqual(read, [])
  })

  test('a save that fails is logged, and the next refresh still runs', async () => {
    const projects = [{ id: 'p1', key: 'OLD', path: '/one' }]
    const logged = []
    let fail = true
    const announced = []
    const refresher = projectkeys.createKeyRefresher({
      projects: () => projects,
      read: async () => 'NEW',
      apply: async (changes) => {
        if (fail) throw new Error('ENOSPC')
        projects[0].key = 'NEW'
        return changes
      },
      onChanged: (applied) => announced.push(applied),
      log: (line) => logged.push(line)
    })
    await refresher.refresh()
    assert.equal(logged.length, 1)
    assert.match(logged[0], /ENOSPC/)
    assert.deepEqual(announced, [])

    fail = false
    await refresher.refresh()
    assert.deepEqual(announced, [[{ id: 'p1', key: 'NEW' }]])
  })
})

describe('isKeyChange', () => {
  test('a board\'s successful add or edit of a key is one', () => {
    assert.equal(projectkeys.isKeyChange({ method: 'POST', url: 'http://127.0.0.1:49152/api/vocabulary/keys', statusCode: 201 }), true)
    assert.equal(projectkeys.isKeyChange({ method: 'PATCH', url: 'http://127.0.0.1:49152/api/vocabulary/keys/NEW', statusCode: 200 }), true)
  })

  test('a read, a refusal, and every other request are not', () => {
    assert.equal(projectkeys.isKeyChange({ method: 'GET', url: 'http://127.0.0.1:49152/api/vocabulary', statusCode: 200 }), false)
    assert.equal(projectkeys.isKeyChange({ method: 'PATCH', url: 'http://127.0.0.1:49152/api/vocabulary/keys/WB', statusCode: 409 }), false)
    assert.equal(projectkeys.isKeyChange({ method: 'PATCH', url: 'http://127.0.0.1:49152/api/vocabulary/statuses/done', statusCode: 200 }), false)
    assert.equal(projectkeys.isKeyChange({ method: 'POST', url: 'http://127.0.0.1:49152/api/vocabulary/keysmith', statusCode: 200 }), false)
    assert.equal(projectkeys.isKeyChange({ method: 'POST', url: 'not a url', statusCode: 200 }), false)
    assert.equal(projectkeys.isKeyChange({}), false)
  })
})
