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
