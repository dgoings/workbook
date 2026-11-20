'use strict'

// Removing a project from Workbench, and a project whose repository is gone.
//
// sidebarcommands.test.js, registry.test.js and presence.test.js hold the
// pieces that load without Electron. None of it reaches the user unless
// main.js wires it to the channel, stops the board, and tells the sidebar
// whether each repository is still there, and the renderer offers the remove
// control and the missing state. main.js and the preload cannot be loaded
// without Electron, so they are read as source with comments removed, the
// way projectkeys-wiring.test.js reads them; app.js's row and removal
// functions are run against the small fake DOM in test/support/tooltips.js.

const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { neighborAfterRemoval, initials } = require('../src/renderer/sidebarmodel')
const { code, fn: appFn, FakeElement } = require('./support/tooltips')

const src = path.join(__dirname, '..', 'src')
const main = code('main', 'main.js')
const preload = code('preload', 'preload.js')
const html = fs.readFileSync(path.join(src, 'renderer', 'index.html'), 'utf8').replace(/\r\n/g, '\n')

function mainFn (name) {
  const match = main.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name} in main.js`)
  return match[0]
}

function constant (name) {
  const at = main.indexOf(`\nconst ${name} = `)
  assert.notEqual(at, -1, `no top-level const ${name} in main.js`)
  const end = main.indexOf('\n\n', at + 1)
  return main.slice(at, end === -1 ? undefined : end)
}

describe('main.js', () => {
  test('project:forget goes through the queued removeProject command', () => {
    assert.match(main, /ipcMain\.handle\('project:forget', \(_event, args\) => sidebarCommands\.removeProject\(args\)\)/)
    // Nowhere else drops a registry entry.
    assert.equal(main.match(/registry\.remove\(/g), null)
  })

  test('a removed project\'s board is closed and what was known about it let go of', () => {
    const body = constant('sidebarCommands')
    assert.match(body, /onRemoved: \(projectId\) => \{[\s\S]*closeProject\(projectId\)[\s\S]*presence\.forget\(projectId\)/)
    assert.match(body, /rebuildMenu: \(\) => installMenu\(\)/)
    assert.match(body, /broadcast: \(payload\) => toChrome\('sidebar:layoutChanged', payload\)/)
    // closeProject stops the server whatever else it finds, and parks the board
    // if it was the one showing.
    const close = mainFn('closeProject')
    assert.match(close, /supervisor\.stop\(projectId\)/)
    assert.match(close, /if \(activeProjectId === projectId\) parkBoard\(\)/)
  })

  test('one presence tracker over the registry, announced on the layout channel', () => {
    const body = constant('presence')
    assert.match(body, /createPresenceTracker\(/)
    assert.match(body, /projects: \(\) => registry\.projects\b/)
    assert.match(body, /onChanged: \(\) => toChrome\('sidebar:layoutChanged', sidebarCommands\.layout\(\)\)/)
    assert.equal(main.match(/createPresenceTracker\(/g).length, 1)
  })

  test('every listed project says whether its repository is missing', () => {
    const body = mainFn('listedProjects')
    assert.match(body, /presence\.get\(project\.id\)/)
    assert.match(body, /missing: /)
    assert.match(body, /missingMessage: /)
  })

  test('a board start looks for the repository first and does not spawn for a missing one', () => {
    const body = mainFn('startProjectServer')
    const look = body.indexOf('presence.refresh([project.id])')
    const spawn = body.indexOf('supervisor.start(project)')
    assert.notEqual(look, -1)
    assert.ok(look < spawn, 'the repository must be looked for before the server is spawned')
    assert.match(body.slice(look, spawn), /if \(found\.missing\) throw new Error\(found\.message\)/)
  })

  test('opening a board looks for the repository before starting it', () => {
    const body = mainFn('layInBoard')
    const look = body.indexOf('presence.refresh([projectId])')
    assert.notEqual(look, -1)
    assert.ok(look < body.indexOf('supervisor.start(project)'))
  })

  test('an open overtaken by the project\'s removal never lays its board in', () => {
    const body = mainFn('layInBoard')
    const afterStart = body.slice(body.indexOf('supervisor.start(project)'))
    assert.match(afterStart, /if \(!registry\.find\(projectId\)\) throw new Error\(/)
    const afterLoad = body.slice(body.indexOf('await view.webContents.loadURL(target)'))
    assert.match(afterLoad, /if \(!registry\.find\(projectId\)\) \{\s*closeProject\(projectId\)\s*throw new Error\(/)
  })

  test('project:presence looks again and answers for an unknown project without throwing', () => {
    const at = main.indexOf("ipcMain.handle('project:presence'")
    assert.notEqual(at, -1)
    const body = main.slice(at, main.indexOf('\n})\n', at))
    assert.match(body, /if \(!registry\.find\(projectId\)\) return \{ known: false/)
    assert.match(body, /await presence\.refresh\(\[projectId\]\)/)
    assert.match(body, /\.\.\.presence\.get\(projectId\)/)
  })

  test('the window coming back into focus and a board stopping look again', () => {
    assert.match(mainFn('createWindow'), /window\.on\('focus', \(\) => \{ refreshPresence\(\) \}\)/)
    const exited = main.slice(main.indexOf("supervisor.on('exited'"))
    assert.match(exited.slice(0, 900), /refreshPresence\(\[projectId\]\)/)
    assert.match(mainFn('refreshPresence'), /presence\.refresh\(ids\)\.catch\(/)
  })
})

describe('the preload', () => {
  test('exposes removal and the presence question on the channels main.js handles', () => {
    assert.match(preload, /forgetProject: \(projectId\) => ipcRenderer\.invoke\('project:forget', \{ projectId \}\)/)
    assert.match(preload, /projectPresence: \(projectId\) => ipcRenderer\.invoke\('project:presence', \{ projectId \}\)/)
  })
})

describe('the page', () => {
  test('the project view has a missing-repository panel with a remove button', () => {
    const section = html.slice(html.indexOf('<section id="view-project"'), html.indexOf('</section>', html.indexOf('<section id="view-project"')))
    for (const id of ['missing-repo', 'missing-heading', 'missing-message', 'missing-remove', 'missing-retry', 'missing-status']) {
      assert.match(section, new RegExp(`id="${id}"`), `#${id} is not in the project view`)
    }
    assert.match(section, /<div id="missing-repo" class="missing-repo" hidden>/)
    assert.match(section, /Remove from Workbench/)
    assert.match(section, /The repository is not touched/)
  })
})

// --- the renderer, run ---------------------------------------------------------

const projects = [
  { id: 'a', key: 'A', name: 'alpha', path: '/src/alpha', status: 'running' },
  { id: 'b', key: 'B', name: 'beta', path: '/src/beta', status: 'stopped', missing: true, missingMessage: 'Repository not found at /src/beta' },
  { id: 'c', key: 'C', name: 'gamma', path: '/src/gamma', status: 'running' }
]

/**
 * projectRow and the removal flow, with the names they touch. `forget` stands
 * in for the main process: it resolves with what removeProject answers.
 */
function renderer ({ forget, view = 'project', active = 'b' } = {}) {
  const calls = { forgets: [], opens: [], views: [], renders: 0, errors: [], chrome: 0 }
  const elements = new Map()
  const el = (id) => {
    if (!elements.has(id)) elements.set(id, new FakeElement('div'))
    return elements.get(id)
  }
  const context = vm.createContext({
    document: { createElement: (tag) => new FakeElement(tag) },
    state: {
      projects: projects.map((project) => ({ ...project })),
      layout: { items: projects.map((project) => ({ kind: 'project', id: project.id })) },
      view,
      activeProjectId: active,
      removingProjectId: null,
      removingCategoryId: null,
      removals: new Set(),
      missingProjectId: null,
      identity: null
    },
    drag: { current: null },
    sidebarModel: { neighborAfterRemoval, initials },
    api: {
      forgetProject: (id) => {
        calls.forgets.push(id)
        return forget ? forget(id) : Promise.resolve({ removed: true, layout: { items: [] }, projects: [] })
      },
      showChrome: () => { calls.chrome += 1 }
    },
    el,
    justDragged: () => false,
    openProject: (id) => { calls.opens.push(id) },
    setView: (name) => { calls.views.push(name) },
    renderProjects: () => { calls.renders += 1 },
    adoptProjects: ({ projects: list }) => { context.state.projects = list },
    hideIdentityForm: () => { context.state.identity = null },
    remoteMessage: (error) => String(error.message),
    console: { error: (...args) => calls.errors.push(args) }
  })
  const names = ['projectRow', 'removeProjectConfirmation', 'buttonText', 'removeProject', 'showMissing', 'hideMissing']
  const built = vm.runInContext(`${names.map(appFn).join('\n')}\n({ ${names.join(', ')} })`, context)
  return { ...built, state: context.state, calls, el }
}

const click = (node, target = node) => {
  for (const listener of node.listeners.get('click') ?? []) listener({ target })
}

describe('a project row', () => {
  test('carries a remove control, named for the project, that only asks first', () => {
    const { projectRow, state, calls } = renderer()
    const row = projectRow(state.projects[0])
    const [remove] = row.find('project-action')
    assert.ok(remove, 'the row has no .project-action')
    assert.equal(remove.tagName, 'BUTTON')
    assert.equal(remove.type, 'button')
    assert.equal(remove.getAttribute('aria-label'), 'Remove alpha from Workbench')
    assert.equal(remove.title, 'Remove from Workbench')
    assert.notEqual(remove.title, row.title)
    click(remove)
    assert.equal(state.removingProjectId, 'a')
    assert.equal(calls.renders, 1)
    assert.deepEqual(calls.forgets, [])
  })

  test('a click on its buttons does not open the project; one elsewhere on the row does', () => {
    const { projectRow, state, calls } = renderer()
    const row = projectRow(state.projects[0])
    const [remove] = row.find('project-action')
    click(row, remove.children[0])
    click(row, remove)
    assert.deepEqual(calls.opens, [])
    click(row, row)
    assert.deepEqual(calls.opens, ['a'])
  })

  test('asking shows "Remove project?" with a confirm that says the repository is not touched', () => {
    const { projectRow, state, calls } = renderer()
    state.removingProjectId = 'a'
    const row = projectRow(state.projects[0])
    const [question] = row.find('project-confirm')
    assert.equal(question.textContent, 'Remove project?')
    assert.equal(row.find('project-name').length, 0, 'the name gives way to the question, as a category\'s does')
    const [yes] = row.find('project-confirm-yes')
    assert.match(yes.title, /Workbench only\. The repository is not touched\./)
    const actions = row.find('project-action')
    assert.equal(actions.length, 2)
    const no = actions.find((action) => action !== yes)
    click(no)
    assert.equal(state.removingProjectId, null)
    assert.deepEqual(calls.forgets, [])
    click(yes)
    assert.deepEqual(calls.forgets, ['a'])
  })

  test('a project whose repository is missing is marked, and its tooltip says where it looked', () => {
    const { projectRow, state } = renderer()
    const row = projectRow(state.projects[1])
    assert.ok(row.classList.contains('missing'))
    const [dot] = row.find('dot')
    assert.equal(dot.className, 'dot missing')
    assert.equal(row.title, 'beta\n/src/beta\nRepository not found at /src/beta')
    // Still removable, like any other.
    assert.equal(row.find('project-action').length, 1)
  })

  test('a present project is not marked', () => {
    const { projectRow, state } = renderer()
    const row = projectRow(state.projects[0])
    assert.ok(!row.classList.contains('missing'))
    assert.equal(row.find('dot')[0].className, 'dot running')
  })
})

describe('removing a project', () => {
  test('asks the main process once, however many clicks arrive while it answers', async () => {
    let release
    const { removeProject, calls } = renderer({
      forget: () => new Promise((resolve) => { release = () => resolve({ removed: true, layout: { items: [] }, projects: [] }) })
    })
    const first = removeProject('b')
    const second = removeProject('b')
    release()
    await Promise.all([first, second])
    assert.deepEqual(calls.forgets, ['b'])
  })

  test('the showing project gives way to the one below it', async () => {
    const remaining = projects.filter((project) => project.id !== 'b')
    const { removeProject, calls } = renderer({
      forget: async () => ({ removed: true, layout: { items: [] }, projects: remaining })
    })
    await removeProject('b')
    assert.deepEqual(calls.opens, ['c'])
  })

  test('the last project removed leaves the empty state', async () => {
    const { removeProject, state, calls } = renderer({
      active: 'a',
      forget: async () => ({ removed: true, layout: { items: [] }, projects: [] })
    })
    state.projects = [state.projects[0]]
    await removeProject('a')
    assert.deepEqual(calls.opens, [])
    assert.deepEqual(calls.views, ['import'])
  })

  test('a project that is not showing is removed without moving the reader', async () => {
    const { removeProject, calls } = renderer({ active: 'a' })
    await removeProject('c')
    assert.deepEqual(calls.forgets, ['c'])
    assert.deepEqual(calls.opens, [])
    assert.deepEqual(calls.views, [])
  })

  test('a removal that fails says so beside the button and keeps the row', async () => {
    const { removeProject, showMissing, state, calls, el } = renderer({
      forget: async () => { throw new Error('disk full') }
    })
    showMissing('b', { missing: true, reason: 'not-found', message: 'Repository not found at /src/beta' })
    await removeProject('b')
    assert.equal(calls.errors.length, 1)
    assert.match(el('missing-status').textContent, /disk full/)
    assert.equal(el('missing-remove').disabled, false)
    assert.equal(state.removals.size, 0, 'a failed removal must not block the next try')
    assert.deepEqual(calls.opens, [])
  })

  test('the missing panel names the problem and is put away once the project is removed', async () => {
    const { removeProject, showMissing, el, calls } = renderer()
    showMissing('b', { missing: true, reason: 'not-a-repository', message: 'No Git repository at /src/beta' })
    assert.equal(el('missing-repo').hidden, false)
    assert.equal(el('missing-heading').textContent, 'Not a Git repository')
    assert.equal(el('missing-message').textContent, 'No Git repository at /src/beta')
    assert.ok(calls.chrome >= 1, 'a board still laid in would cover the panel')
    await removeProject('b')
    assert.equal(el('missing-repo').hidden, true)
  })
})

describe('app.js wiring', () => {
  test('opening a project looks for its repository before asking for an identity or starting it', () => {
    for (const name of ['openProject', 'openNextTask']) {
      const body = appFn(name)
      const look = body.indexOf('await showIfMissing(projectId)')
      assert.notEqual(look, -1, `${name} does not look for the repository`)
      assert.ok(look < body.indexOf('askForIdentity('), `${name} must look before asking for an identity`)
      // And again when the start fails, so a repository deleted since the
      // last look is named rather than reported as a spawn error.
      const failed = body.slice(body.indexOf('} catch (error) {'))
      assert.match(failed, /await showIfMissing\(projectId\)/, `${name} does not look again after a failed start`)
    }
    assert.match(appFn('showIfMissing'), /api\.projectPresence\(projectId\)/)
  })

  test('the panel\'s buttons remove and retry the project it is showing', () => {
    const code = require('./support/tooltips').app
    assert.match(code, /el\('missing-remove'\)\.addEventListener\('click', \(\) => \{\s*if \(state\.missingProjectId\) removeProject\(state\.missingProjectId\)/)
    assert.match(code, /el\('missing-retry'\)\.addEventListener\('click', \(\) => \{\s*if \(state\.missingProjectId\) openProject\(state\.missingProjectId\)/)
  })

  test('a press outside the row being asked about puts the question away', () => {
    const code = require('./support/tooltips').app
    assert.match(code, /state\.removingProjectId !== null &&\s*event\.target\.closest\?\.\('\.project-item'\)\?\.dataset\.projectId !== state\.removingProjectId/)
  })
})
