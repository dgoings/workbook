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

test('a setTheme whose save fails leaves the theme where it was', async () => {
  await withUserData(async (directory) => {
    // A registry the app cannot write at all: its directory's parent is a
    // regular file, so the mkdir every save begins with fails with ENOTDIR.
    // That stands in for the real reasons a save fails — a full disk, a
    // userData directory that is not writable — without needing either.
    const blocked = path.join(directory, 'not-a-directory')
    await fs.writeFile(blocked, '')
    const registry = new Registry(path.join(blocked, 'userData'))
    await registry.load()
    assert.equal(registry.theme, 'system')

    await assert.rejects(registry.setTheme('dark'))
    // save() writes the whole state, so a theme left in memory at the value
    // the file refused would be committed by the next successful save of
    // anything else — a sidebar toggle, an import — and the user would be
    // handed a mode they were never given and never asked for again.
    assert.equal(registry.theme, 'system')
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

// The sidebar layout: order and categories, persisted beside the projects.

const project = (id) => ({ id, name: id.toUpperCase(), path: `/tmp/not-a-real-place/${id}` })

/** Write a registry file holding `projects` and, if given, a stored layout. */
async function writeRegistry (directory, projects, extra = {}) {
  await fs.writeFile(
    path.join(directory, 'registry.json'),
    JSON.stringify({ version: 1, scanRoots: [], projects, ...extra })
  )
}

const ids = (projects) => projects.map((entry) => entry.id)

test('a registry from before categories reads as every project at the top level in stored order', async () => {
  await withUserData(async (directory) => {
    await writeRegistry(directory, [project('c'), project('a'), project('b')])
    const registry = new Registry(directory)
    await registry.load()
    assert.deepEqual(registry.sidebarLayout, {
      items: [{ kind: 'project', id: 'c' }, { kind: 'project', id: 'a' }, { kind: 'project', id: 'b' }]
    })
    assert.deepEqual(ids(registry.orderedProjects), ['c', 'a', 'b'])
  })
})

test('a stored layout of the wrong shape reads as the default', async () => {
  await withUserData(async (directory) => {
    await writeRegistry(directory, [project('a'), project('b')], { sidebarLayout: ['b', 'a'] })
    const registry = new Registry(directory)
    await registry.load()
    assert.deepEqual(registry.sidebarLayout, {
      items: [{ kind: 'project', id: 'a' }, { kind: 'project', id: 'b' }]
    })
    assert.deepEqual(ids(registry.orderedProjects), ['a', 'b'])
  })
})

test('orderedProjects follows the layout, while projects keeps storage order', async () => {
  await withUserData(async (directory) => {
    await writeRegistry(directory, [project('a'), project('b'), project('c')], {
      sidebarLayout: {
        items: [
          { kind: 'project', id: 'c' },
          { kind: 'category', id: 'k', name: 'Work', collapsed: true, projects: ['b', 'a'] }
        ]
      }
    })
    const registry = new Registry(directory)
    await registry.load()
    // The collapsed category's projects still count, in their place.
    assert.deepEqual(ids(registry.orderedProjects), ['c', 'b', 'a'])
    assert.deepEqual(ids(registry.projects), ['a', 'b', 'c'])
    // The entries are the registry's own project objects, not copies.
    assert.equal(registry.orderedProjects[0], registry.find('c'))
  })
})

test('setSidebarLayout saves, survives a reload, and is normalized on the way in', async () => {
  await withUserData(async (directory) => {
    await writeRegistry(directory, [project('a'), project('b'), project('c')])
    const first = new Registry(directory)
    await first.load()
    await first.setSidebarLayout({
      items: [
        { kind: 'category', id: 'k', name: ' Work ', collapsed: false, projects: ['c', 'gone'] },
        { kind: 'project', id: 'a' }
      ]
    })
    const expected = {
      items: [
        { kind: 'category', id: 'k', name: 'Work', collapsed: false, projects: ['c'] },
        { kind: 'project', id: 'a' },
        { kind: 'project', id: 'b' }
      ]
    }
    assert.deepEqual(first.state.sidebarLayout, expected)

    const second = new Registry(directory)
    await second.load()
    assert.deepEqual(second.sidebarLayout, expected)
    assert.deepEqual(ids(second.orderedProjects), ['c', 'a', 'b'])
  })
})

test('a setSidebarLayout whose save fails leaves the layout where it was', async () => {
  await withUserData(async (directory) => {
    // The same unwritable registry as the setTheme case above.
    const blocked = path.join(directory, 'not-a-directory')
    await fs.writeFile(blocked, '')
    const registry = new Registry(path.join(blocked, 'userData'))
    await registry.load()
    registry.state.projects.push(project('a'), project('b'))

    await assert.rejects(registry.setSidebarLayout({
      items: [{ kind: 'project', id: 'b' }, { kind: 'project', id: 'a' }]
    }))
    assert.equal(registry.state.sidebarLayout, undefined)
    assert.deepEqual(ids(registry.orderedProjects), ['a', 'b'])
  })
})

test('an imported project appends at the top level, after every category', async () => {
  await withUserData(async (directory) => {
    await writeRegistry(directory, [project('a'), project('b')], {
      sidebarLayout: {
        items: [
          { kind: 'category', id: 'k', name: 'Work', collapsed: false, projects: ['b'] },
          { kind: 'project', id: 'a' },
          { kind: 'category', id: 'm', name: 'Home', collapsed: true, projects: [] }
        ]
      }
    })
    const registry = new Registry(directory)
    await registry.load()
    await registry.upsert(project('n'))

    const expected = [
      { kind: 'category', id: 'k', name: 'Work', collapsed: false, projects: ['b'] },
      { kind: 'project', id: 'a' },
      { kind: 'category', id: 'm', name: 'Home', collapsed: true, projects: [] },
      { kind: 'project', id: 'n' }
    ]
    // Stored that way, not only read that way.
    assert.deepEqual(registry.state.sidebarLayout.items, expected)
    const reloaded = new Registry(directory)
    await reloaded.load()
    assert.deepEqual(reloaded.sidebarLayout.items, expected)
    assert.deepEqual(ids(reloaded.orderedProjects), ['b', 'a', 'n'])
  })
})

test('updating an existing project keeps its place in the layout', async () => {
  await withUserData(async (directory) => {
    await writeRegistry(directory, [project('a'), project('b')], {
      sidebarLayout: { items: [{ kind: 'project', id: 'b' }, { kind: 'project', id: 'a' }] }
    })
    const registry = new Registry(directory)
    await registry.load()
    await registry.upsert({ id: 'b', path: '/tmp/not-a-real-place/moved' })
    assert.deepEqual(ids(registry.orderedProjects), ['b', 'a'])
    assert.equal(registry.orderedProjects[0].path, '/tmp/not-a-real-place/moved')
  })
})

test('a forgotten project leaves its category, and the stored layout', async () => {
  await withUserData(async (directory) => {
    await writeRegistry(directory, [project('a'), project('b'), project('c')], {
      sidebarLayout: {
        items: [
          { kind: 'project', id: 'a' },
          { kind: 'category', id: 'k', name: 'Work', collapsed: false, projects: ['b', 'c'] }
        ]
      }
    })
    const registry = new Registry(directory)
    await registry.load()
    await registry.remove('b')

    const expected = [
      { kind: 'project', id: 'a' },
      { kind: 'category', id: 'k', name: 'Work', collapsed: false, projects: ['c'] }
    ]
    // Stored that way, not only read that way.
    assert.deepEqual(registry.state.sidebarLayout.items, expected)
    const reloaded = new Registry(directory)
    await reloaded.load()
    assert.deepEqual(reloaded.sidebarLayout.items, expected)
    assert.deepEqual(ids(reloaded.orderedProjects), ['a', 'c'])

    // Imported again, it comes back at the bottom, not in its old category.
    await reloaded.upsert(project('b'))
    assert.deepEqual(ids(reloaded.orderedProjects), ['a', 'c', 'b'])
  })
})
