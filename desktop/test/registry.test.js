'use strict'

// The registry's PATH-notice flag: the one fact the app stores about having
// told the user that `workbook` is now on their PATH.
//
// Registry takes the directory it writes in as a constructor argument, which is
// what makes this testable at all: every case below hands it a fresh mkdtemp
// directory, so nothing here can reach the real userData directory. The module
// reads no environment and no HOME, and nothing in this file loads Electron.

const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')

const { Registry } = require('../src/main/registry')

/** Run one case against a directory of its own, and take it away afterwards. */
async function withUserData (body) {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'workbench-registry-'))
  try {
    await body(directory)
  } finally {
    await fs.rm(directory, { recursive: true, force: true })
  }
}

test('pathNoticeShown is false on a first run, with no registry file at all', async () => {
  await withUserData(async (directory) => {
    const registry = new Registry(directory)
    await registry.load()
    assert.equal(registry.pathNoticeShown, false)
  })
})

test('pathNoticeShown is false for a registry written before the field existed', async () => {
  await withUserData(async (directory) => {
    // What a build from before the PATH install leaves behind: it parses, it
    // has its projects array, and it says nothing about a notice. The strict
    // read is what makes that mean "not shown yet" rather than undefined.
    await fs.writeFile(
      path.join(directory, 'registry.json'),
      JSON.stringify({ version: 1, projects: [], scanRoots: [], theme: 'dark' })
    )
    const registry = new Registry(directory)
    await registry.load()
    assert.equal(registry.pathNoticeShown, false)
    // And the rest of that registry is still there, so reading the new field
    // did not come at the cost of the old ones.
    assert.equal(registry.theme, 'dark')
  })
})

test('pathNoticeShown is false for a stored value that is not the boolean true', async () => {
  await withUserData(async (directory) => {
    // A hand-edited or migrated registry can hold the string. Anything but
    // `true` means the notice has not been shown, which errs toward saying it
    // once more rather than never saying it.
    await fs.writeFile(
      path.join(directory, 'registry.json'),
      JSON.stringify({ version: 1, projects: [], pathNoticeShown: 'true' })
    )
    const registry = new Registry(directory)
    await registry.load()
    assert.equal(registry.pathNoticeShown, false)
  })
})

test('setPathNoticeShown makes it true', async () => {
  await withUserData(async (directory) => {
    const registry = new Registry(directory)
    await registry.load()
    await registry.setPathNoticeShown()
    assert.equal(registry.pathNoticeShown, true)
  })
})

test('setPathNoticeShown survives a reload', async () => {
  await withUserData(async (directory) => {
    const first = new Registry(directory)
    await first.load()
    await first.setPathNoticeShown()

    // The point of storing it in the registry rather than in the renderer: a
    // second launch has to find it already said.
    const second = new Registry(directory)
    await second.load()
    assert.equal(second.pathNoticeShown, true)
  })
})

const INSTALLED = '/tmp/not-a-real-place/Workbench/bin'

test('pendingPathNotice is null until something arms it', async () => {
  await withUserData(async (directory) => {
    const registry = new Registry(directory)
    await registry.load()
    assert.equal(registry.pendingPathNotice, null)
  })
})

test('pendingPathNotice reads null for a stored value that is not a non-empty string', async () => {
  await withUserData(async (directory) => {
    // Nothing is owed unless a real directory is named: an empty string would
    // otherwise reach the renderer as a notice with a blank path in it.
    await fs.writeFile(
      path.join(directory, 'registry.json'),
      JSON.stringify({ version: 1, projects: [], pendingPathNotice: '' })
    )
    const registry = new Registry(directory)
    await registry.load()
    assert.equal(registry.pendingPathNotice, null)
  })
})

test('an armed notice is handed over, and setPathNoticeShown disarms it', async () => {
  await withUserData(async (directory) => {
    const registry = new Registry(directory)
    await registry.load()
    await registry.setPendingPathNotice(INSTALLED)
    assert.equal(registry.pendingPathNotice, INSTALLED)
    assert.equal(registry.pathNoticeShown, false)

    await registry.setPathNoticeShown()
    // Both halves of the one fact: said, and nothing owed.
    assert.equal(registry.pathNoticeShown, true)
    assert.equal(registry.pendingPathNotice, null)
  })
})

test('an armed notice survives a reload while it is still pending', async () => {
  await withUserData(async (directory) => {
    // The whole reason the pending directory exists: this is the launch that
    // changed PATH and then quit before its page could say so.
    const first = new Registry(directory)
    await first.load()
    await first.setPendingPathNotice(INSTALLED)

    const second = new Registry(directory)
    await second.load()
    assert.equal(second.pendingPathNotice, INSTALLED)
    assert.equal(second.pathNoticeShown, false)
  })
})

test('a disarmed notice stays disarmed across a reload', async () => {
  await withUserData(async (directory) => {
    const first = new Registry(directory)
    await first.load()
    await first.setPendingPathNotice(INSTALLED)
    await first.setPathNoticeShown()

    const second = new Registry(directory)
    await second.load()
    assert.equal(second.pathNoticeShown, true)
    assert.equal(second.pendingPathNotice, null)
  })
})

test('setPendingPathNotice does not re-arm once the notice has been shown', async () => {
  await withUserData(async (directory) => {
    const registry = new Registry(directory)
    await registry.load()
    await registry.setPendingPathNotice(INSTALLED)
    await registry.setPathNoticeShown()

    // An app update re-copies the binary and adds its directory again. The user
    // has already read the notice; raising it a second time is a bug.
    await registry.setPendingPathNotice(INSTALLED)
    assert.equal(registry.pendingPathNotice, null)

    // And not even for a different directory, which is what a move of the
    // install location would look like.
    await registry.setPendingPathNotice('/tmp/not-a-real-place/elsewhere/bin')
    assert.equal(registry.pendingPathNotice, null)

    const reloaded = new Registry(directory)
    await reloaded.load()
    assert.equal(reloaded.pendingPathNotice, null)
  })
})

test('arming the same directory twice does not rewrite the registry', async () => {
  await withUserData(async (directory) => {
    const registry = new Registry(directory)
    await registry.load()
    await registry.setPendingPathNotice(INSTALLED)

    const file = path.join(directory, 'registry.json')
    const before = (await fs.stat(file)).mtimeMs
    await registry.setPendingPathNotice(INSTALLED)
    assert.equal((await fs.stat(file)).mtimeMs, before)
    assert.equal(registry.pendingPathNotice, INSTALLED)
  })
})
