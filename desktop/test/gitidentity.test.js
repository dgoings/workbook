'use strict'

// gitidentity.js reads and writes the Git identity a board needs before
// `workbook serve` will start. The runner is injected so most of these cases
// never spawn Git: what is under test is how an unset key is told from a real
// failure, what the form's values are held to, and which `git config` calls a
// save makes. One case at the end does run Git, in a scratch repository with
// its own empty global config, to prove the calls do what they say.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')
const { execFileSync } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const gitidentity = require('../src/main/gitidentity')

// Git's own exit status for `config --get` on a key that is not set.
function unset () {
  return Object.assign(new Error('exit status 1'), { code: 1 })
}

// A fake `git` over an in-memory config: `config --get` answers from it,
// `config --global|--local key value` writes to it, and every call is kept.
function fakeGit (initial = {}) {
  const values = { ...initial }
  const calls = []
  const run = async (_cwd, args) => {
    calls.push(args)
    if (args[1] === '--get') {
      if (!(args[2] in values)) throw unset()
      return `${values[args[2]]}\n`
    }
    values[args[2]] = args[3]
    return ''
  }
  return { run, calls, values }
}

describe('read', () => {
  test('an identity with both halves is complete', async () => {
    const { run } = fakeGit({ 'user.name': 'Ada', 'user.email': 'ada@example.com' })
    assert.deepEqual(await gitidentity.read('/repo', { run }),
      { name: 'Ada', email: 'ada@example.com', complete: true })
  })

  test('a missing email is null and incomplete, not an error', async () => {
    const { run } = fakeGit({ 'user.name': 'Ada' })
    assert.deepEqual(await gitidentity.read('/repo', { run }),
      { name: 'Ada', email: null, complete: false })
  })

  test('nothing set at all reads as two nulls', async () => {
    const { run } = fakeGit()
    assert.deepEqual(await gitidentity.read('/repo', { run }),
      { name: null, email: null, complete: false })
  })

  test('a blank value counts as unset', async () => {
    const { run } = fakeGit({ 'user.name': '  ', 'user.email': 'ada@example.com' })
    assert.equal((await gitidentity.read('/repo', { run })).complete, false)
  })

  test('any other Git failure is thrown rather than read as no identity', async () => {
    const run = async () => { throw Object.assign(new Error('not a git repository'), { code: 128 }) }
    await assert.rejects(gitidentity.read('/repo', { run }), /not a git repository/)
  })
})

describe('validate', () => {
  test('trims what was typed and accepts it', () => {
    assert.deepEqual(gitidentity.validate({ name: '  Ada Lovelace ', email: ' ada@example.com ' }),
      { ok: true, name: 'Ada Lovelace', email: 'ada@example.com', errors: {} })
  })

  test('both halves are required', () => {
    const result = gitidentity.validate({ name: ' ', email: '' })
    assert.equal(result.ok, false)
    assert.ok(result.errors.name)
    assert.ok(result.errors.email)
  })

  test('an address needs one @ with something either side', () => {
    for (const email of ['ada', 'ada@', '@example.com', 'a@b@c', 'ada @example.com']) {
      assert.ok(gitidentity.validate({ name: 'Ada', email }).errors.email, email)
    }
  })

  test('refuses what Git would mangle or read as an option', () => {
    assert.ok(gitidentity.validate({ name: 'Ada <ada>', email: 'ada@example.com' }).errors.name)
    assert.ok(gitidentity.validate({ name: 'Ada\nLovelace', email: 'ada@example.com' }).errors.name)
    assert.ok(gitidentity.validate({ name: '--global', email: 'ada@example.com' }).errors.name)
    assert.ok(gitidentity.validate({ name: 'Ada', email: '-x@example.com' }).errors.email)
  })

  test('holds both halves to a length', () => {
    const long = 'a'.repeat(gitidentity.MAX_NAME + 1)
    assert.ok(gitidentity.validate({ name: long, email: 'ada@example.com' }).errors.name)
    const address = `${'a'.repeat(gitidentity.MAX_EMAIL)}@example.com`
    assert.ok(gitidentity.validate({ name: 'Ada', email: address }).errors.email)
  })

  test('tolerates a missing argument', () => {
    assert.equal(gitidentity.validate().ok, false)
  })
})

describe('write', () => {
  test('a global save sets both keys with --global and reads them back', async () => {
    const git = fakeGit()
    const result = await gitidentity.write('/repo',
      { name: ' Ada ', email: 'ada@example.com', scope: 'global' }, { run: git.run })
    assert.deepEqual(git.calls.slice(0, 2), [
      ['config', '--global', 'user.name', 'Ada'],
      ['config', '--global', 'user.email', 'ada@example.com']
    ])
    assert.deepEqual(result, { name: 'Ada', email: 'ada@example.com', complete: true })
  })

  test('a local save uses --local', async () => {
    const git = fakeGit()
    await gitidentity.write('/repo', { name: 'Ada', email: 'ada@example.com', scope: 'local' }, { run: git.run })
    assert.deepEqual(git.calls[0], ['config', '--local', 'user.name', 'Ada'])
  })

  test('an invalid identity writes nothing', async () => {
    const git = fakeGit()
    await assert.rejects(
      gitidentity.write('/repo', { name: 'Ada', email: 'nope', scope: 'global' }, { run: git.run }),
      /address/)
    assert.equal(git.calls.length, 0)
  })

  test('an unknown scope writes nothing', async () => {
    const git = fakeGit()
    await assert.rejects(
      gitidentity.write('/repo', { name: 'Ada', email: 'ada@example.com', scope: 'system' }, { run: git.run }),
      /scope/)
    assert.equal(git.calls.length, 0)
  })
})

describe('against real Git', () => {
  test('a local save in a repository with no identity makes it complete', async (t) => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'workbench-identity-'))
    t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
    // An empty global config and no system one, so whatever the machine running
    // the tests has configured cannot answer for the repository.
    const saved = { global: process.env.GIT_CONFIG_GLOBAL, nosystem: process.env.GIT_CONFIG_NOSYSTEM }
    process.env.GIT_CONFIG_GLOBAL = path.join(directory, 'empty.gitconfig')
    process.env.GIT_CONFIG_NOSYSTEM = '1'
    fs.writeFileSync(process.env.GIT_CONFIG_GLOBAL, '')
    t.after(() => {
      for (const [key, name] of [['global', 'GIT_CONFIG_GLOBAL'], ['nosystem', 'GIT_CONFIG_NOSYSTEM']]) {
        if (saved[key] === undefined) delete process.env[name]
        else process.env[name] = saved[key]
      }
    })

    const repo = path.join(directory, 'repo')
    execFileSync('git', ['init', '--quiet', repo])

    assert.deepEqual(await gitidentity.read(repo), { name: null, email: null, complete: false })
    const result = await gitidentity.write(repo, { name: 'Ada', email: 'ada@example.com', scope: 'local' })
    assert.deepEqual(result, { name: 'Ada', email: 'ada@example.com', complete: true })
    // And only in the repository: the global file is still empty.
    assert.equal(fs.readFileSync(process.env.GIT_CONFIG_GLOBAL, 'utf8'), '')
  })
})
