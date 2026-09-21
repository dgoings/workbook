'use strict'

// clipath.js puts the bundled CLI on the user's PATH. Every case here runs
// against a freshly minted temporary directory standing in for HOME — never
// os.homedir(), never the real ~/.config or Application Support — because a
// test that wrote into the machine's own shell profile would be a lot worse
// than a test that merely failed. Windows behavior is exercised only through
// the injectable `run`, with a fake: no `reg` or `setx` ever runs here.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const fsSync = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { execFileSync } = require('node:child_process')

const clipath = require('../src/main/clipath')

async function tempDirectory (prefix) {
  return fs.mkdtemp(path.join(os.tmpdir(), `clipath-${prefix}-`))
}

// The one case that needs a real fish to prove anything, and fish is not
// installed everywhere — CI runs these on a Linux runner that has sh and
// nothing else. Skipped rather than failed there: a missing shell is not a
// finding about this code, and the block's text is asserted exactly by the
// case above it either way.
function findFish () {
  const candidates = [
    ...(process.env.PATH ?? '').split(path.delimiter).filter(Boolean).map((directory) => path.join(directory, 'fish')),
    '/opt/homebrew/bin/fish',
    '/usr/local/bin/fish',
    '/usr/bin/fish'
  ]
  return candidates.find((candidate) => {
    try {
      fsSync.accessSync(candidate, fsSync.constants.X_OK)
      return true
    } catch {
      return false
    }
  }) ?? null
}

const fish = findFish()

describe('syncBinary', () => {
  test('a missing installed copy is created, executable, byte-identical', async () => {
    const root = await tempDirectory('missing')
    const bundled = path.join(root, 'bundled', 'workbook')
    await fs.mkdir(path.dirname(bundled), { recursive: true })
    await fs.writeFile(bundled, 'bundled contents')
    await fs.chmod(bundled, 0o755)
    const directory = path.join(root, 'installed')

    const result = await clipath.syncBinary({ bundled, directory })

    assert.equal(result.copied, true)
    assert.equal(result.reason, 'missing')
    assert.equal((await fs.readFile(result.path, 'utf8')), 'bundled contents')
    const stat = await fs.stat(result.path)
    assert.equal(stat.mode & 0o777, 0o755)
  })

  test('a differing bundled binary of the same size is re-copied', async () => {
    const root = await tempDirectory('differs')
    const bundled = path.join(root, 'bundled', 'workbook')
    await fs.mkdir(path.dirname(bundled), { recursive: true })
    await fs.writeFile(bundled, 'AAAA')
    const directory = path.join(root, 'installed')
    await fs.mkdir(directory, { recursive: true })
    const destination = path.join(directory, 'workbook')
    await fs.writeFile(destination, 'BBBB') // same size as bundled, different bytes
    await fs.chmod(destination, 0o755)

    const result = await clipath.syncBinary({ bundled, directory })

    assert.equal(result.reason, 'differs')
    assert.equal(result.copied, true)
    assert.equal((await fs.readFile(result.path, 'utf8')), 'AAAA')
  })

  test('an identical copy is left alone: reason current, mtime unchanged', async () => {
    const root = await tempDirectory('current')
    const bundled = path.join(root, 'bundled', 'workbook')
    await fs.mkdir(path.dirname(bundled), { recursive: true })
    await fs.writeFile(bundled, 'same bytes')
    await fs.chmod(bundled, 0o755)
    const directory = path.join(root, 'installed')
    await fs.mkdir(directory, { recursive: true })
    const destination = path.join(directory, 'workbook')
    await fs.writeFile(destination, 'same bytes')
    await fs.chmod(destination, 0o755)
    const before = await fs.stat(destination)
    await new Promise((resolve) => setTimeout(resolve, 20))

    const result = await clipath.syncBinary({ bundled, directory })

    assert.equal(result.reason, 'current')
    assert.equal(result.copied, false)
    const after = await fs.stat(destination)
    assert.equal(after.mtimeMs, before.mtimeMs)
  })

  test('the copy is atomic: no .tmp file is left behind', async () => {
    const root = await tempDirectory('atomic')
    const bundled = path.join(root, 'bundled', 'workbook')
    await fs.mkdir(path.dirname(bundled), { recursive: true })
    await fs.writeFile(bundled, 'contents')
    const directory = path.join(root, 'installed')

    await clipath.syncBinary({ bundled, directory })

    const entries = await fs.readdir(directory)
    assert.ok(!entries.some((name) => name.endsWith('.tmp')), `left behind: ${entries}`)
  })

  test('a failed rename leaves no .tmp file behind', async () => {
    const root = await tempDirectory('rename-fails')
    const bundled = path.join(root, 'bundled', 'workbook')
    await fs.mkdir(path.dirname(bundled), { recursive: true })
    await fs.writeFile(bundled, 'bundled contents')
    const directory = path.join(root, 'installed')
    // Occupy the destination path with a non-empty directory: fs.rename(file,
    // thisPath) can never succeed, which is what forces syncBinary's rename
    // step to fail after the temp file has already been written.
    const destination = path.join(directory, 'workbook')
    await fs.mkdir(destination, { recursive: true })
    await fs.writeFile(path.join(destination, 'occupied'), 'x')

    await assert.rejects(() => clipath.syncBinary({ bundled, directory }))

    const entries = await fs.readdir(directory)
    assert.ok(!entries.some((name) => name.endsWith('.tmp')), `left behind: ${entries}`)
  })

  test('the installed copy is named for the given platform, not the real host', async () => {
    const root = await tempDirectory('platform-name')
    const bundled = path.join(root, 'bundled', 'workbook')
    await fs.mkdir(path.dirname(bundled), { recursive: true })
    await fs.writeFile(bundled, 'contents')
    const directory = path.join(root, 'installed')

    const result = await clipath.syncBinary({ bundled, directory, platform: 'win32' })

    assert.equal(path.basename(result.path), 'workbook.exe')
  })

  test('a bundled binary that does not exist is skipped by install(), with no directory created', async () => {
    const root = await tempDirectory('absent')
    const home = path.join(root, 'home')
    await fs.mkdir(home, { recursive: true })
    const bundled = path.join(root, 'bundled', 'workbook') // never written

    const result = await clipath.install({ platform: 'darwin', env: {}, home, bundled })

    assert.equal(result.skipped, true)
    assert.equal(result.directory, null)
    const wouldBeDirectory = clipath.installDirectory({ platform: 'darwin', env: {}, home })
    assert.equal(fsSync.existsSync(wouldBeDirectory), false)
  })
})

describe('posixBlock', () => {
  test('appends to PATH, single-quoted, never prepends', () => {
    const block = clipath.posixBlock('/opt/workbench/bin')
    const expected = [
      clipath.MARK_BEGIN,
      'case ":${PATH}:" in',
      "\t*':/opt/workbench/bin:'*) ;;",
      '\t*) PATH="${PATH}:"\'/opt/workbench/bin\' ;;',
      'esac',
      'export PATH',
      clipath.MARK_END
    ].join('\n') + '\n'
    assert.equal(block, expected)
  })

  test('escapes an embedded single quote so the directory stays one literal word', () => {
    const block = clipath.posixBlock("/tmp/o'brien")
    // The standard POSIX trick: close the quote, escape a literal quote, reopen.
    assert.ok(block.includes("':/tmp/o'\\''brien:'"), block)
    assert.ok(block.includes('PATH="${PATH}:"\'/tmp/o\'\\\'\'brien\''), block)
  })

  test('a directory holding a literal single quote still sources cleanly and lands on PATH intact', async () => {
    const root = await tempDirectory('posix-quote')
    const directory = "/tmp/o'brien"
    const block = clipath.posixBlock(directory)
    const scriptFile = path.join(root, 'block.sh')
    await fs.writeFile(scriptFile, block)
    const fakeHome = path.join(root, 'fake-home')
    await fs.mkdir(fakeHome, { recursive: true })

    const output = execFileSync('/bin/sh', ['-c', `. "${scriptFile}"; printf '%s' "$PATH"`],
      { env: { PATH: '/usr/bin:/bin', HOME: fakeHome } }).toString()

    assert.ok(output.split(':').includes(directory), `PATH was: ${output}`)
  })

  test('a directory holding a command substitution or backtick does not execute when sourced', async () => {
    const root = await tempDirectory('posix-injection')
    const marker = path.join(root, 'marker')
    const maliciousDirectory = `/tmp/$(touch ${marker})\`touch ${marker}2\``
    const block = clipath.posixBlock(maliciousDirectory)
    const scriptFile = path.join(root, 'block.sh')
    await fs.writeFile(scriptFile, block)
    const fakeHome = path.join(root, 'fake-home')
    await fs.mkdir(fakeHome, { recursive: true })

    execFileSync('/bin/sh', ['-c', `. "${scriptFile}"; printf '%s' "$PATH"`],
      { env: { PATH: '/usr/bin:/bin', HOME: fakeHome } })

    assert.equal(fsSync.existsSync(marker), false, 'command substitution must not have run')
    assert.equal(fsSync.existsSync(`${marker}2`), false, 'the backtick form must not have run either')
  })
})

describe('fishBlock', () => {
  test('is fish syntax, single-quoted, not the posix block', () => {
    const block = clipath.fishBlock('/opt/workbench/bin')
    const expected = [
      clipath.MARK_BEGIN,
      "if not contains '/opt/workbench/bin' $PATH",
      "    set -gx PATH $PATH '/opt/workbench/bin'",
      'end',
      clipath.MARK_END
    ].join('\n') + '\n'
    assert.equal(block, expected)
    assert.notEqual(block, clipath.posixBlock('/opt/workbench/bin'))
  })

  test('escapes a backslash before a single quote, in that order', () => {
    const block = clipath.fishBlock('/tmp/back\\slash\'quote')
    // fish single quotes: only \ and ' are special, and the backslash itself
    // has to be escaped first or escaping the quote would double-escape it.
    assert.ok(block.includes("'/tmp/back\\\\slash\\'quote'"), block)
  })

  test('a directory holding a fish variable reference does not expand when sourced', { skip: fish ? false : 'fish is not installed here' }, async () => {
    const root = await tempDirectory('fish-injection')
    const maliciousDirectory = '/tmp/$HOME'
    const block = clipath.fishBlock(maliciousDirectory)
    const scriptFile = path.join(root, 'block.fish')
    await fs.writeFile(scriptFile, block)
    const fakeHome = path.join(root, 'fake-home')
    await fs.mkdir(fakeHome, { recursive: true })

    const output = execFileSync(fish, ['-c', `source "${scriptFile}"; printf '%s' "$PATH"`],
      { env: { PATH: '/usr/bin:/bin', HOME: fakeHome } }).toString()

    assert.ok(output.split(':').includes('/tmp/$HOME'), `PATH was: ${output}`)
    assert.ok(!output.includes(fakeHome), `$HOME must not have expanded into PATH: ${output}`)
  })
})

describe('writeBlock', () => {
  test('a missing profile is created with exactly the block', async () => {
    const root = await tempDirectory('write-missing')
    const profile = path.join(root, '.profile')
    const block = clipath.posixBlock('/opt/workbench/bin')

    const result = await clipath.writeBlock(profile, block)

    assert.equal(result.changed, true)
    assert.equal(await fs.readFile(profile, 'utf8'), block)
  })

  test('a profile with no trailing newline keeps its last line intact and gains one', async () => {
    const root = await tempDirectory('write-no-newline')
    const profile = path.join(root, '.profile')
    await fs.writeFile(profile, 'export FOO=bar')
    const block = clipath.posixBlock('/opt/workbench/bin')

    await clipath.writeBlock(profile, block)

    assert.equal(await fs.readFile(profile, 'utf8'), `export FOO=bar\n${block}`)
  })

  test('a second write changes nothing', async () => {
    const root = await tempDirectory('write-idempotent')
    const profile = path.join(root, '.profile')
    const block = clipath.posixBlock('/opt/workbench/bin')
    await clipath.writeBlock(profile, block)
    const before = await fs.readFile(profile, 'utf8')

    const result = await clipath.writeBlock(profile, block)

    assert.equal(result.changed, false)
    assert.equal(await fs.readFile(profile, 'utf8'), before)
  })

  test('a changed directory replaces the block rather than stacking a second one', async () => {
    const root = await tempDirectory('write-replace')
    const profile = path.join(root, '.profile')
    await clipath.writeBlock(profile, clipath.posixBlock('/opt/workbench/bin'))

    await clipath.writeBlock(profile, clipath.posixBlock('/opt/workbench/bin-2'))

    const content = await fs.readFile(profile, 'utf8')
    assert.equal(content.split(clipath.MARK_BEGIN).length - 1, 1, 'exactly one block')
    assert.ok(!content.includes("'/opt/workbench/bin'"), 'the old directory is gone')
    assert.ok(content.includes('/opt/workbench/bin-2'))
  })

  test('preserves an existing profile\'s exact mode, across a write and a repeat write', async () => {
    const root = await tempDirectory('write-mode')
    const profile = path.join(root, '.profile')
    await fs.writeFile(profile, '# secrets live near here\n')
    await fs.chmod(profile, 0o600)
    const block = clipath.posixBlock('/opt/workbench/bin')

    await clipath.writeBlock(profile, block)
    assert.equal((await fs.stat(profile)).mode & 0o777, 0o600, 'mode after the first write')

    await clipath.writeBlock(profile, block) // no-op write; mode must still hold
    assert.equal((await fs.stat(profile)).mode & 0o777, 0o600, 'mode after the second (no-op) write')
  })

  test('the temp file that briefly holds the whole profile is created at mode 0600', async () => {
    const root = await tempDirectory('write-temp-mode')
    const profile = path.join(root, '.profile')
    const block = clipath.posixBlock('/opt/workbench/bin')

    // fs.copyFile is the step writeBlock uses to land the temp file's content
    // onto the real profile; intercepting it here (the exact same
    // node:fs/promises singleton clipath.js itself calls) is the only way to
    // observe the temp file's mode before writeBlock unlinks it.
    const originalCopyFile = fs.copyFile
    let observedMode = null
    fs.copyFile = async (source, destination) => {
      observedMode = (await fs.stat(source)).mode & 0o777
      return originalCopyFile(source, destination)
    }
    try {
      await clipath.writeBlock(profile, block)
    } finally {
      fs.copyFile = originalCopyFile
    }

    assert.equal(observedMode, 0o600)
  })

  test('an unterminated block refuses the write rather than silently dropping the rest of the file', async () => {
    const root = await tempDirectory('write-unterminated')
    const profile = path.join(root, '.profile')
    const original = [
      '# kept',
      clipath.MARK_BEGIN,
      'export IMPORTANT=yes' // no matching MARK_END: a hand-edited or truncated profile
    ].join('\n') + '\n'
    await fs.writeFile(profile, original)

    await assert.rejects(() => clipath.writeBlock(profile, clipath.posixBlock('/opt/workbench/bin')))

    assert.equal(await fs.readFile(profile, 'utf8'), original, 'the file must be byte-identical afterward')
  })

  test('lines above and below the block are untouched, including another tool\'s block', async () => {
    const root = await tempDirectory('write-surrounding')
    const profile = path.join(root, '.profile')
    const other = [
      '# a line before',
      '# >>> other tool >>>',
      'export OTHER=1',
      '# <<< other tool <<<',
      '# a line after',
      ''
    ].join('\n')
    await fs.writeFile(profile, other)

    await clipath.writeBlock(profile, clipath.posixBlock('/opt/workbench/bin'))

    const content = await fs.readFile(profile, 'utf8')
    assert.ok(content.startsWith(other), 'the untouched lines stay exactly as they were')
    assert.ok(content.includes('# >>> other tool >>>'))
    assert.ok(content.includes(clipath.MARK_BEGIN))
  })

  test('sourcing the POSIX block in sh twice puts the directory on PATH once, after an earlier Homebrew entry', async () => {
    const root = await tempDirectory('write-source')
    const appDirectory = '/opt/workbench/bin'
    const block = clipath.posixBlock(appDirectory)
    const scriptFile = path.join(root, 'block.sh')
    await fs.writeFile(scriptFile, block)
    const homebrewDirectory = '/opt/homebrew/bin'
    const fakeHome = path.join(root, 'fake-home') // never read by the block; passed to prove it isn't needed
    await fs.mkdir(fakeHome, { recursive: true })

    const output = execFileSync(
      '/bin/sh',
      ['-c', `. "${scriptFile}"; . "${scriptFile}"; printf '%s' "$PATH"`],
      { env: { PATH: `${homebrewDirectory}:/usr/bin:/bin`, HOME: fakeHome } }
    ).toString()

    const segments = output.split(':')
    const occurrences = segments.filter((segment) => segment === appDirectory).length
    assert.equal(occurrences, 1, `PATH was: ${output}`)
    assert.ok(segments.indexOf(appDirectory) > segments.indexOf(homebrewDirectory), `PATH was: ${output}`)
  })
})

describe('profileTargets', () => {
  test('the POSIX profiles that exist are chosen', () => {
    const home = '/fake/home'
    const present = new Set([path.join(home, '.zshrc'), path.join(home, '.profile')])
    const targets = clipath.profileTargets({ platform: 'darwin', env: {}, home, exists: (file) => present.has(file) })
    assert.deepEqual(targets, [
      { file: path.join(home, '.zshrc'), syntax: 'posix' },
      { file: path.join(home, '.profile'), syntax: 'posix' }
    ])
  })

  test('none existing falls back to ~/.profile', () => {
    const home = '/fake/home'
    const targets = clipath.profileTargets({ platform: 'linux', env: {}, home, exists: () => false })
    assert.deepEqual(targets, [{ file: path.join(home, '.profile'), syntax: 'posix' }])
  })

  test('fish is added when ~/.config/fish exists', () => {
    const home = '/fake/home'
    const fishDirectory = path.join(home, '.config', 'fish')
    const targets = clipath.profileTargets({ platform: 'linux', env: {}, home, exists: (file) => file === fishDirectory })
    assert.ok(targets.some((target) => target.syntax === 'fish' && target.file === path.join(fishDirectory, 'config.fish')))
  })

  test('fish is added when SHELL ends in fish', () => {
    const home = '/fake/home'
    const targets = clipath.profileTargets({ platform: 'linux', env: { SHELL: '/usr/local/bin/fish' }, home, exists: () => false })
    assert.ok(targets.some((target) => target.syntax === 'fish'))
  })

  test('fish is not added otherwise', () => {
    const home = '/fake/home'
    const targets = clipath.profileTargets({ platform: 'linux', env: { SHELL: '/bin/zsh' }, home, exists: () => false })
    assert.ok(!targets.some((target) => target.syntax === 'fish'))
  })

  test('win32 has no profile targets', () => {
    const targets = clipath.profileTargets({ platform: 'win32', env: {}, home: '/fake/home', exists: () => true })
    assert.deepEqual(targets, [])
  })
})

describe('defaultRun', () => {
  test('a command that cannot be launched reports a generic non-zero code, not a crash', async () => {
    // execFile's spawn-time error carries a string error.code (ENOENT), not a
    // process exit code — defaultRun's fallback-to-1 branch is what turns
    // that into the same {code, stdout, stderr} shape every caller expects.
    // Never `reg` or `setx`: this binary does not exist anywhere on PATH.
    const result = await clipath.defaultRun('workbench-clipath-test-nonexistent-command', ['--version'])

    assert.equal(result.code, 1)
    assert.equal(result.stdout, '')
  })
})

describe('updateWindowsPath (fake runner only)', () => {
  test('an absent Path value is created with the directory alone', async () => {
    const calls = []
    const run = async (file, args) => {
      calls.push({ file, args })
      if (args[0] === 'query') {
        return { code: 1, stdout: '', stderr: 'ERROR: The system was unable to find the specified registry key or value.' }
      }
      return { code: 0, stdout: '', stderr: '' }
    }

    const result = await clipath.updateWindowsPath({ directory: 'C:\\Workbench\\bin', run })

    assert.equal(result.changed, true)
    const add = calls.find((call) => call.args[0] === 'add')
    assert.deepEqual(add.args, ['add', 'HKCU\\Environment', '/v', 'Path', '/t', 'REG_SZ', '/d', 'C:\\Workbench\\bin', '/f'])
  })

  test('an existing Path gains ;<dir> and keeps its REG_EXPAND_SZ type', async () => {
    const calls = []
    const run = async (file, args) => {
      calls.push({ file, args })
      if (args[0] === 'query') {
        return { code: 0, stdout: '    Path    REG_EXPAND_SZ    C:\\Existing;%USERPROFILE%\\bin\n', stderr: '' }
      }
      return { code: 0, stdout: '', stderr: '' }
    }

    const result = await clipath.updateWindowsPath({ directory: 'C:\\Workbench\\bin', run })

    assert.equal(result.changed, true)
    const add = calls.find((call) => call.args[0] === 'add')
    assert.deepEqual(add.args, [
      'add', 'HKCU\\Environment', '/v', 'Path', '/t', 'REG_EXPAND_SZ',
      '/d', 'C:\\Existing;%USERPROFILE%\\bin;C:\\Workbench\\bin', '/f'
    ])
  })

  test('a Path already containing the directory, any case, with a trailing separator, is untouched', async () => {
    const run = async (file, args) => {
      if (args[0] === 'query') {
        return { code: 0, stdout: '    Path    REG_SZ    C:\\Existing;C:\\WORKBENCH\\BIN;\n', stderr: '' }
      }
      throw new Error('reg add must not be called when the directory is already present')
    }

    const result = await clipath.updateWindowsPath({ directory: 'C:\\Workbench\\bin', run })

    assert.equal(result.changed, false)
  })

  test('an exit-0 query whose output does not parse throws rather than wiping the PATH', async () => {
    let addCalled = false
    const run = async (file, args) => {
      if (args[0] === 'query') {
        // A shape reg.exe can genuinely produce (a REG_MULTI_SZ Path, or any
        // future format this parser doesn't know) — code 0, but no line this
        // regex recognizes as "Path    REG_SZ    value".
        return { code: 0, stdout: '    Path    REG_MULTI_SZ    C:\\A\\0C:\\B\\0\n', stderr: '' }
      }
      addCalled = true
      return { code: 0, stdout: '', stderr: '' }
    }

    await assert.rejects(() => clipath.updateWindowsPath({ directory: 'C:\\Workbench\\bin', run }))
    assert.equal(addCalled, false, 'reg add must never run against an unparsed value')
  })

  test('the command is reg add, never setx', async () => {
    const files = []
    const run = async (file, args) => {
      files.push(file)
      if (args[0] === 'query') return { code: 1, stdout: '', stderr: '' }
      return { code: 0, stdout: '', stderr: '' }
    }

    await clipath.updateWindowsPath({ directory: 'C:\\Workbench\\bin', run })

    assert.ok(files.length > 0)
    assert.ok(files.every((file) => file === 'reg'))
  })
})

describe('install (orchestrator)', () => {
  async function scenario (prefix) {
    const root = await tempDirectory(prefix)
    const home = path.join(root, 'home')
    await fs.mkdir(home, { recursive: true })
    const bundled = path.join(root, 'resources', 'workbook')
    await fs.mkdir(path.dirname(bundled), { recursive: true })
    await fs.writeFile(bundled, 'bundled cli bytes')
    await fs.chmod(bundled, 0o755)
    return { root, home, bundled }
  }

  test('first run copies, writes the block, and reports changed', async () => {
    const { home, bundled } = await scenario('first-run')

    const result = await clipath.install({ platform: 'darwin', env: {}, home, bundled })

    assert.equal(result.skipped, false)
    assert.equal(result.copied, true)
    assert.equal(result.reason, 'missing')
    assert.equal(result.changed, true)
    assert.equal(result.pathChanged, true, 'a profile actually changed, so the notice may fire')
    assert.ok(result.profiles.length > 0)
    assert.ok(result.profiles.some((profile) => profile.changed))
    assert.deepEqual(result.errors, [])
  })

  test('a second run against the same home does nothing further, down to the bytes on disk', async () => {
    const { home, bundled } = await scenario('second-run')
    const first = await clipath.install({ platform: 'darwin', env: {}, home, bundled })
    const binaryBefore = { stat: await fs.stat(first.binary), bytes: await fs.readFile(first.binary) }
    const profilesBefore = await Promise.all(first.profiles.map(async (profile) => ({
      file: profile.file,
      stat: await fs.stat(profile.file),
      bytes: await fs.readFile(profile.file)
    })))

    const result = await clipath.install({ platform: 'darwin', env: {}, home, bundled })

    assert.equal(result.copied, false)
    assert.equal(result.reason, 'current')
    assert.ok(result.profiles.every((profile) => profile.changed === false))
    assert.equal(result.changed, false)
    assert.equal(result.pathChanged, false)
    assert.deepEqual(result.errors, [])

    const binaryAfter = { stat: await fs.stat(result.binary), bytes: await fs.readFile(result.binary) }
    assert.equal(binaryAfter.stat.mtimeMs, binaryBefore.stat.mtimeMs, 'binary mtime untouched')
    assert.deepEqual(binaryAfter.bytes, binaryBefore.bytes, 'binary bytes untouched')

    for (const before of profilesBefore) {
      const statAfter = await fs.stat(before.file)
      const bytesAfter = await fs.readFile(before.file)
      assert.equal(statAfter.mtimeMs, before.stat.mtimeMs, `${before.file} mtime untouched`)
      assert.deepEqual(bytesAfter, before.bytes, `${before.file} bytes untouched`)
    }
  })

  test('pathChanged stays false when only the binary copied and the Windows PATH write failed', async () => {
    // Proves the split: copying the CLI alone must never tell the user their
    // PATH was edited when it was not.
    const { home, bundled } = await scenario('win32-copy-only')
    const run = async (file, args) => {
      if (args[0] === 'query') return { code: 1, stdout: '', stderr: '' } // no existing value
      return { code: 1, stdout: '', stderr: 'access denied' } // reg add fails
    }

    const result = await clipath.install({ platform: 'win32', env: {}, home, bundled, run })

    assert.equal(result.copied, true)
    assert.equal(path.basename(result.binary), 'workbook.exe')
    assert.equal(result.windows, null, 'updateWindowsPath threw, so no outcome was ever recorded')
    assert.equal(result.changed, true, 'the launch still did something: it copied the CLI')
    assert.equal(result.pathChanged, false, 'no PATH was actually written')
    assert.ok(result.errors.some((message) => message.includes('Windows PATH')))
  })

  test('pathChanged is true when the Windows registry write actually succeeds', async () => {
    const { home, bundled } = await scenario('win32-path-changed')
    const run = async (file, args) => {
      if (args[0] === 'query') return { code: 1, stdout: '', stderr: '' }
      return { code: 0, stdout: '', stderr: '' } // reg add succeeds
    }

    const result = await clipath.install({ platform: 'win32', env: {}, home, bundled, run })

    assert.equal(result.windows.changed, true)
    assert.equal(result.pathChanged, true)
    assert.deepEqual(result.errors, [])
  })

  test('WORKBENCH_SKIP_PATH_SETUP touches nothing', async () => {
    const { home, bundled } = await scenario('skip')

    const result = await clipath.install({ platform: 'darwin', env: { WORKBENCH_SKIP_PATH_SETUP: '1' }, home, bundled })

    assert.equal(result.skipped, true)
    const homeEntries = await fs.readdir(home)
    assert.deepEqual(homeEntries, [])
    const directory = clipath.installDirectory({ platform: 'darwin', env: {}, home })
    assert.equal(fsSync.existsSync(directory), false)
  })

  test('a profile that cannot be written lands in errors and the copy still happens', async () => {
    const { home, bundled } = await scenario('unwritable-profile')
    const profile = path.join(home, '.profile')
    await fs.writeFile(profile, '# existing\n')
    await fs.chmod(profile, 0o400) // read-only: writeBlock's copyFile step must fail

    try {
      const result = await clipath.install({ platform: 'darwin', env: {}, home, bundled })

      assert.equal(result.copied, true)
      assert.ok(result.errors.length > 0, 'expected a recorded error for the unwritable profile')
      assert.ok(result.errors.some((message) => message.includes(profile)))
    } finally {
      await fs.chmod(profile, 0o600) // restore so cleanup can remove the temp directory
    }
  })
})
