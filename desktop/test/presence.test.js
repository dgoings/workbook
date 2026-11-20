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

test('a folder whose .git cannot be read is not called missing', async () => {
  // The folder is there and only its .git refuses to say: permission denied
  // on a checkout someone else owns is not a deleted repository.
  const folder = { isDirectory: () => true }
  const stat = async (target) => {
    if (path.basename(target) === '.git') throw Object.assign(new Error('permission denied'), { code: 'EACCES' })
    return folder
  }
  assert.deepEqual(await checkRepository(path.join('somewhere', 'repo'), { stat }),
    { missing: false, reason: null, message: null })
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

// Settles with `promise`, or fails after `ms`: a check a regression leaves
// hanging fails the test instead of hanging the run.
function within (promise, ms, what) {
  let timer
  const limit = new Promise((resolve, reject) => {
    timer = setTimeout(() => reject(new Error(`${what} did not settle within ${ms}ms`)), ms)
  })
  return Promise.race([promise, limit]).finally(() => clearTimeout(timer))
}

test('the check started last wins, whichever answer arrives last', async () => {
  // A slow look started when the window took the focus finishes after a newer
  // one started by opening the project: the older answer must not overwrite it.
  const projects = [{ id: 'a', path: '/a' }]
  const pending = []
  const presence = createPresenceTracker({
    projects: () => projects,
    check: () => new Promise((resolve) => { pending.push(resolve) }),
    onChanged: () => {}
  })
  const older = presence.refresh(['a'])
  const newer = presence.refresh(['a'])
  assert.equal(pending.length, 2)
  pending[1](gone)
  assert.equal(await within(newer, 1000, 'the newer check'), true)
  pending[0]({ missing: false, reason: null, message: null })
  assert.equal(await within(older, 1000, 'the older check'), false, 'a superseded answer reported a change')
  assert.deepEqual(presence.get('a'), gone, 'the older answer overwrote the newer one')
})

test('a check a newer one has started behind still records its own answer first', async () => {
  // A board start asks, and the window takes the focus before the answer: the
  // start must read what its own look found, not whatever was known before.
  const projects = [{ id: 'a', path: '/a' }]
  const pending = []
  const presence = createPresenceTracker({
    projects: () => projects,
    check: () => new Promise((resolve) => { pending.push(resolve) }),
    onChanged: () => {}
  })
  const older = presence.refresh(['a'])
  const newer = presence.refresh(['a'])
  pending[0](gone)
  assert.equal(await within(older, 1000, 'the older check'), true)
  assert.deepEqual(presence.get('a'), gone, 'the overtaken check read a stale answer')
  pending[1]({ missing: false, reason: null, message: null })
  assert.equal(await within(newer, 1000, 'the newer check'), true)
  assert.equal(presence.get('a').missing, false, 'the newer answer did not replace the older one')
})

test('a check that hangs gives up in time and reads as present, not missing', async () => {
  // A network mount that never answers must not leave "Starting the board…"
  // up forever, and not knowing is not the same as missing: the board start
  // goes on, as for a path that cannot be read.
  const projects = [{ id: 'a', path: '/a' }, { id: 'b', path: '/b' }]
  const presence = createPresenceTracker({
    projects: () => projects,
    check: (dir) => dir === '/a' ? new Promise(() => {}) : Promise.resolve(gone),
    onChanged: () => {},
    timeoutMs: 20
  })
  assert.equal(await within(presence.refresh(), 1000, 'a refresh over a hung check'), true)
  assert.equal(presence.get('a').missing, false)
  assert.equal(presence.get('b').missing, true)
})

test('the default time limit is a few seconds', () => {
  const { CHECK_TIMEOUT_MS } = require('../src/main/presence')
  assert.ok(CHECK_TIMEOUT_MS >= 1000 && CHECK_TIMEOUT_MS <= 10000, `CHECK_TIMEOUT_MS is ${CHECK_TIMEOUT_MS}`)
})
