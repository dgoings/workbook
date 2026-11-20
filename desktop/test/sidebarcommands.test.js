'use strict'

// The sidebar layout commands behind the sidebar:* IPC channels, run against a
// real Registry in a temp directory: what a bad request is told, what a good
// one saves and announces, and that back-to-back commands — a drag fires them
// that way — each see the one before, even when a save in the middle fails.

const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')

const { Registry } = require('../src/main/registry')
const { createSidebarCommands } = require('../src/main/sidebarcommands')

const project = (id) => ({ id, key: id.toUpperCase(), name: id, path: `/tmp/not-a-real-place/${id}` })
const top = (id) => ({ kind: 'project', id })

/**
 * A loaded registry holding a, b, c and a Work category with b, and the
 * commands over it, with every side effect recorded.
 */
async function withCommands (body) {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'workbench-sidebar-'))
  try {
    await fs.writeFile(path.join(directory, 'registry.json'), JSON.stringify({
      version: 1,
      projects: [project('a'), project('b'), project('c')],
      sidebarLayout: {
        items: [top('a'), { kind: 'category', id: 'work', name: 'Work', collapsed: false, projects: ['b'] }, top('c')]
      }
    }))
    const registry = new Registry(directory)
    await registry.load()
    const broadcasts = []
    let menus = 0
    let minted = 0
    const commands = createSidebarCommands({
      registry,
      mintId: () => `minted-${++minted}`,
      listProjects: () => registry.orderedProjects.map((entry) => ({ ...entry, running: true })),
      rebuildMenu: () => { menus += 1 },
      broadcast: (payload) => broadcasts.push(payload)
    })
    await body({ registry, commands, broadcasts, menus: () => menus, directory })
  } finally {
    await fs.rm(directory, { recursive: true, force: true })
  }
}

const ids = (list) => list.map((entry) => entry.id)

test('sidebar:layout answers with the layout and the ordered, decorated project list', async () => {
  await withCommands(async ({ commands }) => {
    const { layout, projects } = commands.layout()
    assert.deepEqual(layout.items.map((item) => item.id), ['a', 'work', 'c'])
    assert.deepEqual(ids(projects), ['a', 'b', 'c'])
    assert.equal(projects[0].running, true)
  })
})

test('a move is applied, saved, announced once, and rebuilds the menu', async () => {
  await withCommands(async ({ commands, broadcasts, menus, directory }) => {
    const answer = await commands.moveProject({ projectId: 'c', target: { kind: 'category', id: 'work', index: 0 } })
    const expected = [top('a'), { kind: 'category', id: 'work', name: 'Work', collapsed: false, projects: ['c', 'b'] }]
    assert.deepEqual(answer.layout.items, expected)
    assert.deepEqual(ids(answer.projects), ['a', 'c', 'b'])
    assert.equal(answer.projects[0].running, true)
    assert.deepEqual(broadcasts, [{ layout: answer.layout, projects: answer.projects }])
    assert.equal(menus(), 1)

    const reloaded = new Registry(directory)
    await reloaded.load()
    assert.deepEqual(reloaded.sidebarLayout.items, expected)
  })
})

test('every command does what its channel names', async () => {
  await withCommands(async ({ registry, commands, broadcasts, menus }) => {
    const created = await commands.createCategory({ name: '  Later ' })
    assert.equal(created.categoryId, 'minted-1')
    assert.deepEqual(created.layout.items.at(-1),
      { kind: 'category', id: 'minted-1', name: 'Later', collapsed: false, projects: [] })

    await commands.moveCategory({ categoryId: 'minted-1', index: 0 })
    assert.equal(registry.sidebarLayout.items[0].id, 'minted-1')

    await commands.renameCategory({ categoryId: 'minted-1', name: 'Soon' })
    assert.equal(registry.sidebarLayout.items[0].name, 'Soon')

    await commands.setCategoryCollapsed({ categoryId: 'work', collapsed: true })
    assert.equal(registry.sidebarLayout.items[2].collapsed, true)

    await commands.moveProject({ projectId: 'b', target: { kind: 'top', index: 0 } })
    assert.deepEqual(ids(registry.orderedProjects), ['b', 'a', 'c'])

    const deleted = await commands.deleteCategory({ categoryId: 'minted-1' })
    assert.deepEqual(deleted.layout.items.map((item) => item.id), ['b', 'a', 'work', 'c'])

    assert.equal(broadcasts.length, 6)
    assert.equal(menus(), 6)
  })
})

test('a bad request is rejected with the argument it got wrong, and nothing is saved or announced', async () => {
  await withCommands(async ({ registry, commands, broadcasts, menus, directory }) => {
    const before = registry.sidebarLayout
    const file = path.join(directory, 'registry.json')
    const stored = await fs.readFile(file, 'utf8')
    const cases = [
      [commands.moveProject, null, /arguments must be an object/],
      [commands.moveProject, { projectId: 5, target: { kind: 'top', index: 0 } }, /projectId must be a non-empty string/],
      [commands.moveProject, { projectId: 'zz', target: { kind: 'top', index: 0 } }, /projectId "zz" is not a project/],
      [commands.moveProject, { projectId: 'a' }, /target must be an object/],
      [commands.moveProject, { projectId: 'a', target: { kind: 'side', index: 0 } }, /target\.kind/],
      [commands.moveProject, { projectId: 'a', target: { kind: 'category', index: 0 } }, /target\.id must be/],
      [commands.moveProject, { projectId: 'a', target: { kind: 'category', id: 'zz', index: 0 } }, /target\.id "zz" is not a category/],
      [commands.moveProject, { projectId: 'a', target: { kind: 'top', index: '1' } }, /target\.index must be an integer/],
      [commands.moveProject, { projectId: 'a', target: { kind: 'top', index: 1.5 } }, /target\.index must be an integer/],
      [commands.moveCategory, { categoryId: 'zz', index: 0 }, /categoryId "zz" is not a category/],
      [commands.moveCategory, { categoryId: 'a', index: 0 }, /categoryId "a" is not a category/],
      [commands.moveCategory, { categoryId: 'work', index: NaN }, /index must be an integer/],
      [commands.moveCategory, { index: 0 }, /categoryId must be a non-empty string/],
      [commands.createCategory, { name: '   ' }, /name must be a non-empty string/],
      [commands.createCategory, { name: 3 }, /name must be a non-empty string/],
      [commands.createCategory, undefined, /arguments must be an object/],
      [commands.renameCategory, { categoryId: 'work', name: '' }, /name must be a non-empty string/],
      [commands.renameCategory, { categoryId: 'zz', name: 'X' }, /categoryId "zz" is not a category/],
      [commands.setCategoryCollapsed, { categoryId: 'work', collapsed: 'true' }, /collapsed must be a boolean/],
      [commands.setCategoryCollapsed, { categoryId: 'zz', collapsed: true }, /categoryId "zz" is not a category/],
      [commands.deleteCategory, { categoryId: 'zz' }, /categoryId "zz" is not a category/],
      [commands.deleteCategory, { categoryId: '' }, /categoryId must be a non-empty string/]
    ]
    for (const [command, args, message] of cases) {
      await assert.rejects(command(args), message, `${command.name} ${JSON.stringify(args)}`)
    }
    assert.deepEqual(registry.sidebarLayout, before)
    assert.equal(await fs.readFile(file, 'utf8'), stored)
    assert.deepEqual(broadcasts, [])
    assert.equal(menus(), 0)
  })
})

test('commands fired back to back each see the one before', async () => {
  await withCommands(async ({ registry, commands, broadcasts }) => {
    // Not awaited one by one: that is how a drag arrives. Unqueued, both
    // creates would read the same layout and the second save would drop the
    // first category, and the rename would find no category to rename.
    const settled = await Promise.all([
      commands.createCategory({ name: 'One' }),
      commands.createCategory({ name: 'Two' }),
      commands.renameCategory({ categoryId: 'minted-1', name: 'First' }),
      commands.moveProject({ projectId: 'a', target: { kind: 'category', id: 'minted-2', index: 0 } })
    ])
    assert.deepEqual(registry.sidebarLayout.items.slice(-2), [
      { kind: 'category', id: 'minted-1', name: 'First', collapsed: false, projects: [] },
      { kind: 'category', id: 'minted-2', name: 'Two', collapsed: false, projects: ['a'] }
    ])
    assert.equal(broadcasts.length, 4)
    // Each answer is the state its own command left, in order.
    assert.equal(settled[0].layout.items.length, 4)
    assert.equal(settled[1].layout.items.length, 5)
  })
})

test('a failed save in the middle rejects that command alone; the next one builds on the last saved layout', async () => {
  await withCommands(async ({ registry, commands, broadcasts, menus }) => {
    const realSave = registry.save.bind(registry)
    let saves = 0
    registry.save = () => {
      saves += 1
      return saves === 2 ? Promise.reject(new Error('disk full')) : realSave()
    }

    const first = commands.createCategory({ name: 'Kept' })
    const second = commands.moveProject({ projectId: 'c', target: { kind: 'top', index: 0 } })
    const third = commands.renameCategory({ categoryId: 'work', name: 'Job' })
    await first
    await assert.rejects(second, /disk full/)
    await third

    // The failed move is gone from memory as well as from the file; the
    // create before it and the rename after it both stand.
    assert.deepEqual(registry.sidebarLayout.items, [
      top('a'),
      { kind: 'category', id: 'work', name: 'Job', collapsed: false, projects: ['b'] },
      top('c'),
      { kind: 'category', id: 'minted-1', name: 'Kept', collapsed: false, projects: [] }
    ])
    // Nothing was announced for it, so the renderer never drew it.
    assert.equal(broadcasts.length, 2)
    assert.equal(menus(), 2)
    assert.deepEqual(broadcasts.at(-1).layout, registry.sidebarLayout)
  })
})

// Removing a project from Workbench: sidebar:removeProject, which the
// project:forget channel runs. It drops the registry entry and its place in the
// sidebar, hands the project to onRemoved (main.js stops its board there), and
// announces the result the way every layout edit is announced.

async function withRemovals (body) {
  await withCommands(async (context) => {
    const removed = []
    const commands = createSidebarCommands({
      registry: context.registry,
      mintId: () => 'unused',
      listProjects: () => context.registry.orderedProjects,
      rebuildMenu: () => { context.menuCount = (context.menuCount ?? 0) + 1 },
      broadcast: (payload) => context.broadcasts.push(payload),
      onRemoved: (projectId) => removed.push(projectId)
    })
    await body({ ...context, commands, removed, menus: () => context.menuCount ?? 0 })
  })
}

test('removeProject drops the project from the registry and its category, then announces it', async () => {
  await withRemovals(async ({ registry, commands, broadcasts, removed, menus, directory }) => {
    const answer = await commands.removeProject({ projectId: 'b' })
    assert.equal(answer.removed, true)
    assert.deepEqual(ids(answer.projects), ['a', 'c'])
    assert.deepEqual(answer.layout.items, [
      top('a'), { kind: 'category', id: 'work', name: 'Work', collapsed: false, projects: [] }, top('c')
    ])
    assert.deepEqual(removed, ['b'])
    assert.equal(menus(), 1)
    assert.deepEqual(broadcasts, [{ layout: answer.layout, projects: answer.projects }])
    assert.equal(registry.find('b'), null)

    const reloaded = new Registry(directory)
    await reloaded.load()
    assert.deepEqual(ids(reloaded.orderedProjects), ['a', 'c'])
  })
})

test('removing an unknown or already-removed project is a quiet no-op', async () => {
  await withRemovals(async ({ commands, broadcasts, removed, menus }) => {
    const answer = await commands.removeProject({ projectId: 'nobody' })
    assert.equal(answer.removed, false)
    assert.deepEqual(ids(answer.projects), ['a', 'b', 'c'])
    assert.deepEqual(removed, [])
    assert.equal(menus(), 0)
    assert.deepEqual(broadcasts, [])
  })
})

test('two removes of the same project at once remove it once', async () => {
  await withRemovals(async ({ commands, broadcasts, removed }) => {
    const [first, second] = await Promise.all([
      commands.removeProject({ projectId: 'a' }),
      commands.removeProject({ projectId: 'a' })
    ])
    assert.equal(first.removed, true)
    assert.equal(second.removed, false)
    assert.deepEqual(removed, ['a'])
    assert.equal(broadcasts.length, 1)
  })
})

test('a remove queues behind a layout edit and the edit after it sees the removal', async () => {
  await withRemovals(async ({ registry, commands }) => {
    const move = commands.moveProject({ projectId: 'c', target: { kind: 'category', id: 'work', index: 0 } })
    const remove = commands.removeProject({ projectId: 'c' })
    const later = commands.moveProject({ projectId: 'c', target: { kind: 'top', index: 0 } })
    await move
    await remove
    await assert.rejects(later, /is not a project/)
    assert.deepEqual(ids(registry.orderedProjects), ['a', 'b'])
  })
})

test('a remove whose save fails rejects, keeps the project, and stops nothing', async () => {
  await withRemovals(async ({ registry, commands, broadcasts, removed }) => {
    registry.save = () => Promise.reject(new Error('disk full'))
    await assert.rejects(commands.removeProject({ projectId: 'b' }), /disk full/)
    assert.notEqual(registry.find('b'), null)
    assert.deepEqual(removed, [])
    assert.deepEqual(broadcasts, [])
  })
})

test('removeProject refuses a malformed request before it queues', async () => {
  await withRemovals(async ({ commands }) => {
    for (const args of [undefined, null, [], {}, { projectId: '' }, { projectId: 7 }]) {
      await assert.rejects(commands.removeProject(args), /sidebar:removeProject: /)
    }
  })
})

test('a failing onRemoved does not undo the removal or hide it from the sidebar', async () => {
  await withCommands(async ({ registry, broadcasts }) => {
    const commands = createSidebarCommands({
      registry,
      mintId: () => 'unused',
      listProjects: () => registry.orderedProjects,
      rebuildMenu: () => {},
      broadcast: (payload) => broadcasts.push(payload),
      onRemoved: () => { throw new Error('the window is gone') },
      log: () => {}
    })
    const answer = await commands.removeProject({ projectId: 'a' })
    assert.equal(answer.removed, true)
    assert.equal(registry.find('a'), null)
    assert.equal(broadcasts.length, 1)
  })
})
