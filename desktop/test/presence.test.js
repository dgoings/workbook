'use strict'

// Whether each project's repository is still where the registry says it is.
// A repository deleted, moved, or on a drive that is not mounted has to show
// as missing in the sidebar rather than as a board that will not start, and
// it must never be removed for it: the drive may come back. These run against
// temporary directories only, and build every path with path.join, so they
// hold on Windows as well.

const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')

const { checkRepository, createPresenceTracker } = require('../src/main/presence')

async function withDirectory (body) {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'workbench-presence-'))
  try {
    await body(directory)
  } finally {
    await fs.rm(directory, { recursive: true, force: true })
  }
}

test('a checkout with a .git directory is present', async () => {
  await withDirectory(async (directory) => {
    await fs.mkdir(path.join(directory, '.git'))
    assert.deepEqual(await checkRepository(directory), { missing: false, reason: null, message: null })
  })
})

test('a worktree or submodule, whose .git is a file, is present', async () => {
  await withDirectory(async (directory) => {
    await fs.writeFile(path.join(directory, '.git'), 'gitdir: /somewhere/else\n')
    assert.equal((await checkRepository(directory)).missing, false)
  })
})

test('a path with nothing at it is missing, and says where it looked', async () => {
  await withDirectory(async (directory) => {
    const gone = path.join(directory, 'deleted')
    const found = await checkRepository(gone)
    assert.equal(found.missing, true)
    assert.equal(found.reason, 'not-found')
    assert.equal(found.message, `Repository not found at ${gone}`)
  })
})

test('a path under a file, or that is a file, is missing', async () => {
  await withDirectory(async (directory) => {
    const file = path.join(directory, 'file')
    await fs.writeFile(file, '')
    for (const target of [file, path.join(file, 'below')]) {
      const found = await checkRepository(target)
      assert.equal(found.missing, true, target)
      assert.equal(found.message, `Repository not found at ${target}`)
    }
  })
})

test('a folder that is no longer a Git repository is missing, for that reason', async () => {
  await withDirectory(async (directory) => {
    const found = await checkRepository(directory)
    assert.equal(found.missing, true)
    assert.equal(found.reason, 'not-a-repository')
    assert.equal(found.message, `No Git repository at ${directory}`)
  })
})

test('a path that cannot be read for some other reason is not called missing', async () => {
  // EACCES, EIO and the like say nothing certain; the board's own start
  // reports the real problem, and nothing is marked missing on a guess.
  const stat = async () => { throw Object.assign(new Error('permission denied'), { code: 'EACCES' }) }
  assert.equal((await checkRepository(path.join('anywhere'), { stat })).missing, false)
})

test('a path that is not a non-empty string is missing rather than a throw', async () => {
  for (const value of [undefined, null, '', 42]) {
    assert.equal((await checkRepository(value)).missing, true)
  }
})

function tracker (projects, answers) {
  const changes = []
  const checked = []
  const presence = createPresenceTracker({
    projects: () => projects,
    check: async (dir) => {
      checked.push(dir)
      return answers.get(dir) ?? { missing: false, reason: null, message: null }
    },
    onChanged: () => changes.push(true)
  })
  return { presence, changes, checked }
}

const gone = { missing: true, reason: 'not-found', message: 'Repository not found at /b' }

test('an unchecked project reads as present', () => {
  const { presence } = tracker([], new Map())
  assert.deepEqual(presence.get('nobody'), { missing: false, reason: null, message: null })
})

test('refresh checks every project, records what it found, and announces only a change', async () => {
  const projects = [{ id: 'a', path: '/a' }, { id: 'b', path: '/b' }]
  const answers = new Map([['/b', gone]])
  const { presence, changes, checked } = tracker(projects, answers)

  assert.equal(await presence.refresh(), true)
  assert.deepEqual(checked.sort(), ['/a', '/b'])
  assert.equal(presence.get('a').missing, false)
  assert.deepEqual(presence.get('b'), gone)
  assert.equal(changes.length, 1)

  // The same answers again change nothing, and say nothing.
  assert.equal(await presence.refresh(), false)
  assert.equal(changes.length, 1)

  // The drive comes back: the project is present again, and that is a change.
  answers.delete('/b')
  assert.equal(await presence.refresh(['b']), true)
  assert.equal(presence.get('b').missing, false)
  assert.equal(changes.length, 2)
})

test('refresh with ids checks only those projects, and skips an unknown id', async () => {
  const projects = [{ id: 'a', path: '/a' }, { id: 'b', path: '/b' }]
  const { presence, checked } = tracker(projects, new Map())
  await presence.refresh(['b', 'removed'])
  assert.deepEqual(checked, ['/b'])
})

test('a project removed while it was being checked is not recorded', async () => {
  const projects = [{ id: 'a', path: '/a' }]
  let release
  const presence = createPresenceTracker({
    projects: () => projects,
    check: () => new Promise((resolve) => { release = () => resolve(gone) }),
    onChanged: () => { throw new Error('nothing should be announced') }
  })
  const pending = presence.refresh()
  projects.length = 0
  presence.forget('a')
  release()
  assert.equal(await pending, false)
  assert.equal(presence.get('a').missing, false)
})

test('forget drops what was known about a project, so one imported again starts fresh', async () => {
  const projects = [{ id: 'b', path: '/b' }]
  const { presence } = tracker(projects, new Map([['/b', gone]]))
  await presence.refresh()
  assert.equal(presence.get('b').missing, true)
  presence.forget('b')
  assert.equal(presence.get('b').missing, false)
  presence.forget('never-known')
})

test('a check that throws reads as present, and the refresh still settles', async () => {
  const projects = [{ id: 'a', path: '/a' }, { id: 'b', path: '/b' }]
  const presence = createPresenceTracker({
    projects: () => projects,
    check: async (dir) => { if (dir === '/a') throw new Error('boom'); return gone },
    onChanged: () => {}
  })
  assert.equal(await presence.refresh(), true)
  assert.equal(presence.get('a').missing, false)
  assert.equal(presence.get('b').missing, true)
})
