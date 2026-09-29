'use strict'

// gitidentity.js reads and writes the Git identity a board needs before
// `workbook serve` will start, and reports which half of it Git cannot supply on
// this machine: the address whenever `user.email` is unset, and the name only
// where Git cannot derive one from the operating-system account. The runner is
// injected so most of these cases never spawn Git: what is under test is how an
// unset key is told from a real failure, how `git var GIT_AUTHOR_IDENT` and the
// two keys decide what is needed, what the form's values are held to, and which
// `git config` calls a save makes. The cases at the end do run Git, in a scratch
// repository with its own empty global config, to prove the calls do what they
// say.
//
// Those cases never read the test machine's own account. Git's name fallback is
// the account's full name on macOS, the display name on Windows, and empty on a
// Linux account with no GECOS field — which is what the ubuntu-24.04 runner has,
// and what made an earlier version of this suite pass here and fail there. So
// scratchRepository pins the fallback in both directions rather than asking the
// machine: GIT_AUTHOR_NAME where a name should be derivable, and
// `user.useConfigOnly` where none should be. See its comment for why that setting
// and not an empty GIT_AUTHOR_NAME, which the Windows leg would have read as
// unset.

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

// And what `git var GIT_AUTHOR_IDENT` says when it has no name to use.
function emptyIdentName () {
  return Object.assign(new Error('fatal: empty ident name (for <>) not allowed'), { code: 128 })
}

/**
 * A fake `git` over an in-memory config.
 *
 * `config --get` answers from it, `config --global|--local key value` writes to
 * it, and `var GIT_AUTHOR_IDENT` assembles the two the way Git does: `user.name`
 * when it is set, otherwise `accountName`, which stands for the name Git derives
 * from the operating-system account. Pass null for a machine whose derivation is
 * empty, and `git var` fails as it does there.
 *
 * `calls` is every call in order; `writes` is only the ones that set a value, so
 * a test can say nothing was written without counting the reads that precede it.
 */
function fakeGit (initial = {}, accountName = 'Account Name') {
  const values = { ...initial }
  const calls = []
  const writes = []
  const run = async (_cwd, args) => {
    calls.push(args)
    if (args[0] === 'var') {
      const name = values['user.name'] || accountName
      if (!name) throw emptyIdentName()
      return `${name} <${values['user.email'] ?? ''}> 1700000000 +0000\n`
    }
    if (args[1] === '--get') {
      if (!(args[2] in values)) throw unset()
      return `${values[args[2]]}\n`
    }
    values[args[2]] = args[3]
    writes.push(args)
    return ''
  }
  return { run, calls, writes, values }
}

describe('read', () => {
  test('an identity with both halves is complete', async () => {
    const { run } = fakeGit({ 'user.name': 'Ada', 'user.email': 'ada@example.com' })
    assert.deepEqual(await gitidentity.read('/repo', { run }),
      { name: 'Ada', email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
  })

  test('a missing email is null and incomplete, not an error', async () => {
    const { run } = fakeGit({ 'user.name': 'Ada' })
    assert.deepEqual(await gitidentity.read('/repo', { run }),
      { name: 'Ada', email: null, complete: false, needs: { email: true, name: false } })
  })

  test('nothing set at all needs the address, and the name where Git has none', async () => {
    const derivable = fakeGit()
    assert.deepEqual(await gitidentity.read('/repo', { run: derivable.run }),
      { name: null, email: null, complete: false, needs: { email: true, name: false } })

    const bare = fakeGit({}, null)
    assert.deepEqual(await gitidentity.read('/repo', { run: bare.run }),
      { name: null, email: null, complete: false, needs: { email: true, name: true } })
  })

  test('a blank address counts as unset', async () => {
    const { run } = fakeGit({ 'user.name': 'Ada', 'user.email': '  ' })
    assert.equal((await gitidentity.read('/repo', { run })).complete, false)
  })

  // Production mutation: requiring a name wherever one is unset is what stopped a
  // board that `workbook serve` would have opened. On a machine that can derive
  // one, the address is the whole requirement.
  test('an email with no name is complete where Git can name an author', async () => {
    const { run } = fakeGit({ 'user.email': 'ada@example.com' })
    assert.deepEqual(await gitidentity.read('/repo', { run }),
      { name: null, email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
  })

  // Production mutation: taking the email alone as the requirement is what let a
  // user past this form and into `fatal: empty ident name` from `workbook create`.
  test('an email with no name needs the name where Git cannot derive one', async () => {
    const { run } = fakeGit({ 'user.email': 'ada@example.com' }, null)
    assert.deepEqual(await gitidentity.read('/repo', { run }),
      { name: null, email: 'ada@example.com', complete: false, needs: { email: false, name: true } })
  })

  // A configured name is a name whatever the account holds, so the machine's
  // derivation never comes into it.
  test('a configured name is enough even where the derivation is empty', async () => {
    const { run } = fakeGit({ 'user.name': 'Ada', 'user.email': 'ada@example.com' }, null)
    assert.equal((await gitidentity.read('/repo', { run })).complete, true)
  })

  test('the address is needed even where Git would invent one for a commit', async () => {
    // `git var` answers, because Git will sign a commit with a hostname-derived
    // address; `workbook serve` reads `git config --get user.email` and gets
    // nothing, so this still needs asking about.
    const run = async (_cwd, args) => {
      if (args[0] === 'var') return 'Account Name <you@host.example.com> 1700000000 +0000\n'
      throw unset()
    }
    assert.deepEqual((await gitidentity.read('/repo', { run })).needs, { email: true, name: false })
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

  test('by default the address is required and the name is not', () => {
    const missing = gitidentity.validate({ name: ' ', email: '' })
    assert.equal(missing.ok, false)
    assert.ok(missing.errors.email)
    assert.equal(missing.errors.name, undefined)

    assert.deepEqual(gitidentity.validate({ name: '  ', email: 'ada@example.com' }),
      { ok: true, name: '', email: 'ada@example.com', errors: {} })
  })

  test('a needed name is required, and a name that is not needed is not', () => {
    const missing = gitidentity.validate({ name: ' ', email: 'ada@example.com' },
      { email: false, name: true })
    assert.equal(missing.ok, false)
    assert.equal(missing.errors.name, 'Enter a name.')
    assert.equal(missing.errors.email, undefined)

    assert.equal(gitidentity.validate({ name: 'Ada', email: 'ada@example.com' },
      { email: false, name: true }).ok, true)
  })

  test('an address that is not needed may be left out, but not left wrong', () => {
    assert.equal(gitidentity.validate({ name: 'Ada', email: '' },
      { email: false, name: true }).ok, true)
    assert.ok(gitidentity.validate({ name: 'Ada', email: 'nope' },
      { email: false, name: true }).errors.email)
  })

  test('both are required when Git can supply neither', () => {
    const missing = gitidentity.validate({}, { email: true, name: true })
    assert.ok(missing.errors.name)
    assert.ok(missing.errors.email)
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

  test('holds a name that was given, and the address, to a length', () => {
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
    assert.deepEqual(git.writes, [
      ['config', '--global', 'user.name', 'Ada'],
      ['config', '--global', 'user.email', 'ada@example.com']
    ])
    assert.deepEqual(result,
      { name: 'Ada', email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
  })

  // Production mutation: writing an empty user.name would leave the checkout
  // worse than it started — Git refuses to commit with an empty ident name — and
  // on a machine that derives one the form does not ask.
  test('a save with no name writes the address alone', async () => {
    const git = fakeGit()
    const result = await gitidentity.write('/repo',
      { name: '  ', email: 'ada@example.com', scope: 'global' }, { run: git.run })
    assert.deepEqual(git.writes, [['config', '--global', 'user.email', 'ada@example.com']])
    assert.deepEqual(result,
      { name: null, email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
  })

  // The other half of the same rule: what is already configured is not rewritten
  // just because the form carried it.
  test('a save writes only the field that was filled in', async () => {
    const git = fakeGit({ 'user.email': 'ada@example.com' }, null)
    const result = await gitidentity.write('/repo',
      { name: 'Ada', email: '', scope: 'local' }, { run: git.run })
    assert.deepEqual(git.writes, [['config', '--local', 'user.name', 'Ada']])
    assert.equal(result.complete, true)
  })

  // Production mutation: writing every non-empty field is what copied the global
  // address into .git/config on a name-only save.
  test('a value that was not changed is left alone', async () => {
    const git = fakeGit({ 'user.email': 'ada@example.com' }, null)
    await gitidentity.write('/repo',
      { name: 'Ada', email: 'ada@example.com', scope: 'local' }, { run: git.run })
    assert.deepEqual(git.writes, [['config', '--local', 'user.name', 'Ada']])
  })

  test('a value that was changed is written even when Git did not need it', async () => {
    const git = fakeGit({ 'user.name': 'Ada', 'user.email': 'ada@example.com' })
    await gitidentity.write('/repo',
      { name: 'Ada Lovelace', email: 'ada@example.com', scope: 'global' }, { run: git.run })
    assert.deepEqual(git.writes, [['config', '--global', 'user.name', 'Ada Lovelace']])
  })

  test('a name Git cannot supply is refused rather than written as nothing', async () => {
    const git = fakeGit({ 'user.email': 'ada@example.com' }, null)
    await assert.rejects(
      gitidentity.write('/repo', { name: '  ', email: '', scope: 'local' }, { run: git.run }),
      /Enter a name\./)
    assert.deepEqual(git.writes, [])
  })

  test('a local save uses --local', async () => {
    const git = fakeGit()
    await gitidentity.write('/repo', { name: 'Ada', email: 'ada@example.com', scope: 'local' }, { run: git.run })
    assert.deepEqual(git.writes[0], ['config', '--local', 'user.name', 'Ada'])
  })

  test('an invalid identity writes nothing', async () => {
    const git = fakeGit()
    await assert.rejects(
      gitidentity.write('/repo', { name: 'Ada', email: 'nope', scope: 'global' }, { run: git.run }),
      /address/)
    assert.deepEqual(git.writes, [])
  })

  test('an unknown scope reads nothing and writes nothing', async () => {
    const git = fakeGit()
    await assert.rejects(
      gitidentity.write('/repo', { name: 'Ada', email: 'ada@example.com', scope: 'system' }, { run: git.run }),
      /scope/)
    assert.equal(git.calls.length, 0)
  })
})

/**
 * A repository Git can read and nothing else can answer for, and the environment
 * every Git call about it is made in.
 *
 * Nothing here touches `process.env`. These cases run concurrently — node:test
 * runs the tests of a suite that way — and a shared environment would let one
 * case's fallback decide another's answer, which is exactly the thing being
 * pinned. The environment is built once per repository and handed to the module
 * as `env`, so each case spawns Git with its own.
 *
 * GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM give the repository an empty global
 * config and no system one, so whatever the machine running the tests has
 * configured cannot stand in for what the case sets.
 *
 * `derivable` is the one input that differs by machine: whether Git can produce a
 * name for an account with no `user.name`. macOS hands over the account's full
 * name, Windows the account's display name, and a Linux account with no GECOS
 * field — the ubuntu-24.04 runner's — hands over nothing. Neither branch is left
 * to the machine:
 *
 *  - derivable: GIT_AUTHOR_NAME and GIT_COMMITTER_NAME are set to a name, which
 *    Git prefers over both the configuration and the account. Non-empty, so it
 *    survives into the child on every platform.
 *  - not derivable: `user.useConfigOnly` is set in the repository, which turns the
 *    account derivation off outright — Git then fails with "no name was given and
 *    auto-detection is disabled" — and the two variables are removed from the
 *    environment so an ambient value cannot supply a name behind it.
 *
 * Deliberately not an empty GIT_AUTHOR_NAME, which is what this suite used first.
 * Windows has no empty-valued environment variables — the C runtime reports one as
 * unset — so on the windows-2025 leg Git would have fallen back to the account
 * name and every case expecting no derivation would have failed there and nowhere
 * else. `user.useConfigOnly` is configuration rather than an environment trick, so
 * it behaves the same everywhere, and it is a setting real teams use to keep Git
 * from authoring commits as an account name nobody chose, so these cases cover
 * that configuration as well as standing in for a bare account.
 */
function scratchRepository (t, { derivable = true } = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'workbench-identity-'))
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))

  const globalConfig = path.join(directory, 'empty.gitconfig')
  fs.writeFileSync(globalConfig, '')
  const env = { ...process.env, GIT_CONFIG_GLOBAL: globalConfig, GIT_CONFIG_NOSYSTEM: '1' }
  if (derivable) {
    env.GIT_AUTHOR_NAME = 'Runner'
    env.GIT_COMMITTER_NAME = 'Runner'
  } else {
    delete env.GIT_AUTHOR_NAME
    delete env.GIT_COMMITTER_NAME
  }

  const repo = path.join(directory, 'repo')
  execFileSync('git', ['init', '--quiet', repo], { env })
  // Every direct Git call in a case goes through this, so it sees the same
  // environment the module under test is given.
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { env }).toString().trim()
  if (!derivable) {
    git('config', '--local', 'user.useConfigOnly', 'true')
    assertNoDerivableName(repo, env)
  }
  return { repo, env, git, globalConfig }
}

/**
 * Check the premise of every "Git cannot name an author" case, once per repository.
 *
 * If the setup above ever stops taking — a Git that reads `user.useConfigOnly`
 * differently, an environment that supplies a name another way — the cases would
 * otherwise fail as a handful of confusing `needs` mismatches. This says what
 * actually went wrong instead.
 */
function assertNoDerivableName (repo, env) {
  // The same probe the module makes, address and all, so a failure here is about
  // the name for the same reason a failure there is.
  const probe = { ...env, GIT_AUTHOR_EMAIL: 'probe@workbench.invalid' }
  let answered
  try {
    answered = execFileSync('git', ['-C', repo, 'var', 'GIT_AUTHOR_IDENT'],
      { env: probe, stdio: ['pipe', 'pipe', 'pipe'] }).toString().trim()
  } catch {
    return
  }
  assert.fail('this repository is set up so that Git cannot derive a name, but ' +
    `\`git var GIT_AUTHOR_IDENT\` answered "${answered}". The cases below test what ` +
    'the form asks for when Git has no name to offer, and they cannot on this machine.')
}

// One key as .git/config alone holds it, ignoring the global file, or null when
// that file does not set it. `--local --get` exits 1 for a key that is not there.
function localValue (repo, env, key) {
  try {
    return execFileSync('git', ['-C', repo, 'config', '--local', '--get', key],
      { env, stdio: ['pipe', 'pipe', 'pipe'] }).toString().trim()
  } catch {
    return null
  }
}

// `git commit-tree` against the empty tree: either the hash it wrote, or the
// complaint it refused with. The one real check that an identity this module
// calls complete is one Git will actually sign a commit with.
function commitEmptyTree (repo, env) {
  const tree = execFileSync('git', ['-C', repo, 'hash-object', '-t', 'tree', '--stdin'],
    { env, input: '' }).toString().trim()
  try {
    const commit = execFileSync('git', ['-C', repo, 'commit-tree', '-m', 'probe', tree],
      { env, stdio: ['pipe', 'pipe', 'pipe'] }).toString().trim()
    return { ok: true, commit, stderr: '' }
  } catch (error) {
    return { ok: false, commit: '', stderr: String(error.stderr ?? '') }
  }
}

describe('against real Git', () => {
  // (a) Both halves configured: nothing to ask, and Git signs a commit.
  test('both halves configured is complete and needs nothing', async (t) => {
    const { repo, env, git } = scratchRepository(t)
    git('config', 'user.name', 'Ada')
    git('config', 'user.email', 'ada@example.com')

    assert.deepEqual(await gitidentity.read(repo, { env }),
      { name: 'Ada', email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
    const committed = commitEmptyTree(repo, env)
    assert.ok(committed.ok, committed.stderr)
  })

  // (b) The case the previous commit read correctly and generalized wrongly: on a
  // machine that derives a name, the address alone is the whole requirement, and
  // Git agrees by signing a commit with a name that is nowhere in the config.
  test('an address with no name is complete where Git derives one, and Git commits', async (t) => {
    const { repo, env, git } = scratchRepository(t)
    git('config', 'user.email', 'ada@example.com')

    assert.deepEqual(await gitidentity.read(repo, { env }),
      { name: null, email: 'ada@example.com', complete: true, needs: { email: false, name: false } })

    const committed = commitEmptyTree(repo, env)
    assert.ok(committed.ok, committed.stderr)
    assert.match(committed.commit, /^[0-9a-f]{40,64}$/)
  })

  // The other half of "no more than necessary": a fresh checkout on a machine
  // that derives a name is asked for the address alone. `git var` cannot answer
  // here either — it has no address to use — which is why the name probe supplies
  // one, so that a missing address is never read as a missing name.
  test('a fresh checkout that can derive a name is asked only for the address', async (t) => {
    const { repo, env } = scratchRepository(t)

    assert.deepEqual((await gitidentity.read(repo, { env })).needs, { email: true, name: false })

    const result = await gitidentity.write(repo, { email: 'ada@example.com', scope: 'local' }, { env })
    assert.deepEqual(result,
      { name: null, email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
    const committed = commitEmptyTree(repo, env)
    assert.ok(committed.ok, committed.stderr)
  })

  // (c) The same configuration on a machine whose derivation is empty, which is
  // the ubuntu-24.04 runner. Git refuses the commit, so the form has to ask, and
  // the name is the only thing it asks for.
  test('an address with no name needs the name where the fallback is empty', async (t) => {
    const { repo, env, git } = scratchRepository(t, { derivable: false })
    git('config', 'user.email', 'ada@example.com')

    assert.deepEqual(await gitidentity.read(repo, { env }),
      { name: null, email: 'ada@example.com', complete: false, needs: { email: false, name: true } })

    const committed = commitEmptyTree(repo, env)
    assert.equal(committed.ok, false)
    assert.match(committed.stderr, /auto-detection is disabled/)
  })

  // (d) A fresh install of Git on such a machine: neither half, and Git can
  // supply neither.
  test('nothing configured and an empty fallback needs both', async (t) => {
    const { repo, env } = scratchRepository(t, { derivable: false })

    assert.deepEqual(await gitidentity.read(repo, { env }),
      { name: null, email: null, complete: false, needs: { email: true, name: true } })
  })

  // (e) And saving what was asked for is what makes the check pass — once with
  // the name alone, once with both.
  test('saving only what was needed makes the check pass', async (t) => {
    const { repo, env, git } = scratchRepository(t, { derivable: false })
    git('config', 'user.email', 'ada@example.com')

    const result = await gitidentity.write(repo, { name: 'Ada', email: '', scope: 'local' }, { env })
    assert.deepEqual(result,
      { name: 'Ada', email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
    // And Git agrees: the name that was just written is the one thing it was
    // missing, so the commit it refused in case (c) now goes through.
    const committed = commitEmptyTree(repo, env)
    assert.ok(committed.ok, committed.stderr)
  })

  // The form prefills from what Git resolves, so a name-only save arrives carrying
  // the global address in the email box. Writing that back would copy the address
  // into .git/config, where it would outlive a later change to the global one —
  // this repository would go on recording an address the user had moved off.
  test('a local save of the name alone leaves the global address where it is', async (t) => {
    const { repo, env, git, globalConfig } = scratchRepository(t, { derivable: false })
    git('config', '--global', 'user.email', 'ada@example.com')

    assert.deepEqual((await gitidentity.read(repo, { env })).needs, { email: false, name: true })
    // The name is what was asked for; the address is what the form prefilled.
    const result = await gitidentity.write(repo,
      { name: 'Ada', email: 'ada@example.com', scope: 'local' }, { env })
    assert.equal(result.complete, true)

    assert.equal(git('config', '--local', '--get', 'user.name'), 'Ada')
    assert.equal(localValue(repo, env, 'user.email'), null)
    assert.match(fs.readFileSync(globalConfig, 'utf8'), /ada@example\.com/)
  })

  test('a local save in a repository with no identity makes it complete', async (t) => {
    const { repo, env, globalConfig } = scratchRepository(t, { derivable: false })

    assert.deepEqual((await gitidentity.read(repo, { env })).needs, { email: true, name: true })
    const result = await gitidentity.write(repo,
      { name: 'Ada', email: 'ada@example.com', scope: 'local' }, { env })
    assert.deepEqual(result,
      { name: 'Ada', email: 'ada@example.com', complete: true, needs: { email: false, name: false } })
    // And only in the repository: the global file is still empty.
    assert.equal(fs.readFileSync(globalConfig, 'utf8'), '')
  })
})
