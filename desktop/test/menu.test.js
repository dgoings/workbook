'use strict'

// menu.js builds the application menu as plain data so the bindings can be
// asserted here without Electron: which chord does what, which items are
// offered, and which are disabled when there is nothing for them to act on.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { buildMenuTemplate } = require('../src/main/menu')

function actionsSpy () {
  const calls = []
  const spy = {}
  for (const name of ['selectProject', 'stepProject', 'showNext', 'showImport', 'cycleTheme', 'toggleSidebar', 'reloadBoard', 'boardCommand']) {
    spy[name] = (...args) => calls.push([name, ...args])
  }
  return { spy, calls }
}

function flatten (template) {
  const items = []
  const visit = (list) => list.forEach((item) => { items.push(item); if (item.submenu) visit(item.submenu) })
  visit(template)
  return items
}

function byAccelerator (template, accelerator) {
  const found = flatten(template).filter((item) => item.accelerator === accelerator)
  assert.equal(found.length, 1, `expected exactly one item bound to ${accelerator}`)
  return found[0]
}

const projects = [
  { id: 'a', key: 'AA', name: 'Alpha' },
  { id: 'b', key: 'BB', name: 'Beta' },
  { id: 'c', key: 'CC', name: 'Gamma' }
]

describe('buildMenuTemplate', () => {
  test('binds every chord the design names, once', () => {
    const { spy } = actionsSpy()
    const template = buildMenuTemplate({ platform: 'darwin', projects, activeProjectId: 'b', nextAvailable: true, actions: spy })
    for (const chord of ['CmdOrCtrl+B', 'CmdOrCtrl+Shift+D', 'CmdOrCtrl+0', 'CmdOrCtrl+Shift+I', 'CmdOrCtrl+Alt+Up', 'CmdOrCtrl+Alt+Down', 'CmdOrCtrl+N', 'CmdOrCtrl+F', 'CmdOrCtrl+,', 'CmdOrCtrl+[', 'CmdOrCtrl+]', 'CmdOrCtrl+R', 'CmdOrCtrl+1', 'CmdOrCtrl+2', 'CmdOrCtrl+3']) {
      byAccelerator(template, chord)
    }
    assert.equal(flatten(template).filter((item) => item.accelerator === 'CmdOrCtrl+4').length, 0)
  })

  test('project items name the project and select it by index', () => {
    const { spy, calls } = actionsSpy()
    const template = buildMenuTemplate({ platform: 'darwin', projects, activeProjectId: null, nextAvailable: true, actions: spy })
    const second = byAccelerator(template, 'CmdOrCtrl+2')
    assert.match(second.label, /Beta/)
    assert.match(second.label, /BB/)
    second.click()
    assert.deepEqual(calls, [['selectProject', 1]])
  })

  test('offers at most nine project items', () => {
    const many = Array.from({ length: 12 }, (_, i) => ({ id: String(i), key: 'K' + i, name: 'P' + i }))
    const { spy, calls } = actionsSpy()
    const template = buildMenuTemplate({ platform: 'darwin', projects: many, activeProjectId: null, nextAvailable: true, actions: spy })
    for (const item of flatten(template)) {
      if (typeof item.click === 'function') item.click()
    }
    const selections = calls.filter(([name]) => name === 'selectProject').map(([, index]) => index)
    assert.deepEqual(selections, [0, 1, 2, 3, 4, 5, 6, 7, 8])
    const doubleDigitAccelerators = flatten(template).filter((item) => /^CmdOrCtrl\+\d{2,}$/.test(item.accelerator || ''))
    assert.deepEqual(doubleDigitAccelerators, [])
  })

  test('a project name or key carrying & is escaped on win32/linux and left alone on darwin', () => {
    const withAmpersand = [{ id: 'r', key: 'RD', name: 'R&D' }]
    const { spy } = actionsSpy()
    const win = buildMenuTemplate({ platform: 'win32', projects: withAmpersand, activeProjectId: null, nextAvailable: true, actions: spy })
    assert.match(byAccelerator(win, 'CmdOrCtrl+1').label, /R&&D/)
    const mac = buildMenuTemplate({ platform: 'darwin', projects: withAmpersand, activeProjectId: null, nextAvailable: true, actions: spy })
    assert.match(byAccelerator(mac, 'CmdOrCtrl+1').label, /R&D \(RD\)/)
  })

  test('board actions are disabled without an active board and enabled with one', () => {
    const { spy, calls } = actionsSpy()
    const idle = buildMenuTemplate({ platform: 'darwin', projects, activeProjectId: null, nextAvailable: true, actions: spy })
    for (const chord of ['CmdOrCtrl+N', 'CmdOrCtrl+F', 'CmdOrCtrl+,', 'CmdOrCtrl+[', 'CmdOrCtrl+]', 'CmdOrCtrl+R']) {
      assert.equal(byAccelerator(idle, chord).enabled, false, chord)
    }
    const active = buildMenuTemplate({ platform: 'darwin', projects, activeProjectId: 'a', nextAvailable: true, actions: spy })
    byAccelerator(active, 'CmdOrCtrl+N').click()
    byAccelerator(active, 'CmdOrCtrl+F').click()
    byAccelerator(active, 'CmdOrCtrl+,').click()
    byAccelerator(active, 'CmdOrCtrl+[').click()
    byAccelerator(active, 'CmdOrCtrl+]').click()
    byAccelerator(active, 'CmdOrCtrl+R').click()
    assert.deepEqual(calls, [
      ['boardCommand', 'new-task'], ['boardCommand', 'search'], ['boardCommand', 'config'],
      ['boardCommand', 'back'], ['boardCommand', 'forward'], ['reloadBoard']
    ])
  })

  test('the Next item follows nextAvailable and the steps carry their direction', () => {
    const { spy, calls } = actionsSpy()
    const one = buildMenuTemplate({ platform: 'darwin', projects: projects.slice(0, 1), activeProjectId: null, nextAvailable: false, actions: spy })
    assert.equal(byAccelerator(one, 'CmdOrCtrl+0').enabled, false)
    const two = buildMenuTemplate({ platform: 'darwin', projects, activeProjectId: null, nextAvailable: true, actions: spy })
    byAccelerator(two, 'CmdOrCtrl+0').click()
    byAccelerator(two, 'CmdOrCtrl+Alt+Up').click()
    byAccelerator(two, 'CmdOrCtrl+Alt+Down').click()
    byAccelerator(two, 'CmdOrCtrl+Shift+I').click()
    byAccelerator(two, 'CmdOrCtrl+Shift+D').click()
    byAccelerator(two, 'CmdOrCtrl+B').click()
    assert.deepEqual(calls, [['showNext'], ['stepProject', -1], ['stepProject', 1], ['showImport'], ['cycleTheme'], ['toggleSidebar']])
  })

  test('keeps the standard editing and window roles so text fields keep working', () => {
    const { spy } = actionsSpy()
    const mac = buildMenuTemplate({ platform: 'darwin', projects, activeProjectId: null, nextAvailable: true, actions: spy })
    const roles = flatten(mac).map((item) => item.role).filter(Boolean)
    for (const role of ['undo', 'redo', 'cut', 'copy', 'paste', 'selectAll', 'minimize', 'close', 'quit', 'about', 'hide']) {
      assert.ok(roles.includes(role), role)
    }
    const win = buildMenuTemplate({ platform: 'win32', projects, activeProjectId: null, nextAvailable: true, actions: spy })
    const winRoles = flatten(win).map((item) => item.role).filter(Boolean)
    assert.ok(!winRoles.includes('hide'))
    assert.ok(winRoles.includes('quit'))
  })

  test('Import keeps off the devtools chord on win32/linux', () => {
    const { spy, calls } = actionsSpy()
    // Ctrl+Shift+I opens devtools on Windows and Linux, so Import cannot have
    // it there; on macOS devtools is Cmd+Alt+I and Cmd+Shift+I is free.
    const mac = buildMenuTemplate({ platform: 'darwin', projects, activeProjectId: null, nextAvailable: true, actions: spy })
    byAccelerator(mac, 'CmdOrCtrl+Shift+I').click()
    assert.equal(flatten(mac).filter((item) => item.accelerator === 'CmdOrCtrl+Shift+O').length, 0)
    for (const platform of ['win32', 'linux']) {
      const template = buildMenuTemplate({ platform, projects, activeProjectId: null, nextAvailable: true, actions: spy })
      byAccelerator(template, 'CmdOrCtrl+Shift+O').click()
      assert.equal(flatten(template).filter((item) => item.accelerator === 'CmdOrCtrl+Shift+I').length, 0, platform)
    }
    assert.deepEqual(calls, [['showImport'], ['showImport'], ['showImport']])
  })
})
