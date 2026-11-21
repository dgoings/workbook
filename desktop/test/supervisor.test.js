'use strict'

// The supervisor's start awaits the binary's location before it spawns, and
// for that moment it holds nothing for the project: a stop arriving then found
// nothing to stop, and the spawn that followed was a server for a project the
// app had already let go of. These load the real supervisor with the binary
// lookup held open and spawn replaced, and count what was spawned.

const EventEmitter = require('node:events')
const Module = require('node:module')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')
const assert = require('node:assert/strict')

const SUPERVISOR = path.join(__dirname, '..', 'src', 'main', 'supervisor.js')

function load () {
  const h = { spawned: [], lookups: [] }
  const stubs = {
    './workbook': {
      resolveBinary: () => new Promise((resolve) => { h.lookups.push(() => resolve('/bin/workbook')) })
    },
    'node:child_process': {
      execFileSync () { return '' },
      spawn (binary, args, options) {
        const child = new EventEmitter()
        child.pid = 4242
        child.killed = false
        child.exitCode = null
        child.signalCode = null
        child.stdout = new EventEmitter()
        child.stderr = new EventEmitter()
        child.kill = () => { child.killed = true; return true }
        h.spawned.push({ binary, args, cwd: options.cwd, child })
        return child
      }
    }
  }
  const original = Module._load
  Module._load = function (request, parent, isMain) {
    if (parent?.filename === SUPERVISOR && Object.hasOwn(stubs, request)) return stubs[request]
    return original.call(this, request, parent, isMain)
  }
  try {
    delete require.cache[SUPERVISOR]
    const { Supervisor } = require(SUPERVISOR)
    return { h, supervisor: new Supervisor(null) }
  } finally {
    Module._load = original
    delete require.cache[SUPERVISOR]
  }
}

const project = { id: 'A', path: path.join(os.tmpdir(), 'workbench-supervisor-A') }

const tick = () => new Promise((resolve) => setImmediate(resolve))

// Every child spawned is closed when its test ends, so a start a regression
// left waiting for a banner settles at once instead of on the supervisor's
// fifteen-second bind timeout.
function closeAfter (t, h) {
  t.after(() => {
    for (const { child } of h.spawned) child.emit('close', null)
  })
}

test('a stop that arrives while the binary is being found means nothing is spawned', async (t) => {
  const { h, supervisor } = load()
  closeAfter(t, h)
  const starting = supervisor.start(project)
  starting.catch(() => {})
  assert.equal(h.lookups.length, 1)
  supervisor.stop('A')
  h.lookups[0]()
  await tick()
  assert.deepEqual(h.spawned.map((entry) => entry.cwd), [], 'a server was spawned after its project was stopped')
  await assert.rejects(starting, /stopped before it started/)
  assert.equal(supervisor.status('A').status, 'stopped')
})

test('a start asked for after that stop still spawns', async (t) => {
  const { h, supervisor } = load()
  closeAfter(t, h)
  const first = supervisor.start(project)
  first.catch(() => {})
  supervisor.stop('A')
  const second = supervisor.start(project)
  second.catch(() => {})
  for (const release of h.lookups) release()
  await tick()
  assert.equal(h.spawned.length, 1)
  await assert.rejects(first, /stopped before it started/)
  h.spawned[0].child.stderr.emit('data', Buffer.from('Workbook board: http://127.0.0.1:7331\n'))
  assert.equal(await second, 'http://127.0.0.1:7331')
})

test('a start with no stop in between spawns once and resolves with the bound address', async (t) => {
  const { h, supervisor } = load()
  closeAfter(t, h)
  const starting = supervisor.start(project)
  h.lookups[0]()
  await tick()
  assert.equal(h.spawned.length, 1)
  assert.equal(h.spawned[0].cwd, project.path)
  h.spawned[0].child.stderr.emit('data', Buffer.from('Workbook board: http://127.0.0.1:7332\n'))
  assert.equal(await starting, 'http://127.0.0.1:7332')
})
