#!/usr/bin/env node
'use strict'

// The checks Workbench ran inline in its own CI, so they can run from a
// checkout and from this repository's CI without being copied into YAML.
//
// Each catches a mistake this project actually made: an edit that removed a
// function while leaving it in module.exports, which throws only when something
// first calls it; a renderer that reaches for an element id the markup no longer
// has; and a preload that invokes a channel the main process stopped handling.

const fs = require('node:fs')
const path = require('node:path')
const { execFileSync } = require('node:child_process')

const root = path.join(__dirname, '..')
const main = path.join(root, 'src', 'main')
const renderer = path.join(root, 'src', 'renderer')
const preload = path.join(root, 'src', 'preload', 'preload.js')

let failures = 0
function fail (message) {
  failures += 1
  console.error(message)
}

// Every source file parses.
const boardPreload = path.join(root, 'src', 'preload', 'board.js')
const sources = [
  ...fs.readdirSync(main).map((name) => path.join(main, name)),
  preload,
  boardPreload,
  path.join(renderer, 'app.js'),
  ...fs.readdirSync(path.join(root, 'scripts'))
    .filter((name) => name.endsWith('.js'))
    .map((name) => path.join(root, 'scripts', name))
]
for (const file of sources) {
  try {
    execFileSync(process.execPath, ['--check', file], { stdio: 'pipe' })
  } catch (error) {
    fail(`${path.relative(root, file)} does not parse:\n${error.stderr}`)
  }
}

// Every export of every main-process module that can be loaded outside
// Electron is defined. main.js and updater.js need the electron module and are
// covered by the parse check above.
for (const name of fs.readdirSync(main)) {
  if (name === 'main.js' || name === 'updater.js') continue
  let module
  try {
    module = require(path.join(main, name))
  } catch (error) {
    fail(`${name} does not load outside Electron: ${error.message}`)
    continue
  }
  const missing = Object.entries(module)
    .filter(([, value]) => value === undefined)
    .map(([key]) => key)
  if (missing.length > 0) fail(`${name} exports undefined: ${missing.join(', ')}`)
}

// The renderer references only element ids that exist.
const html = fs.readFileSync(path.join(renderer, 'index.html'), 'utf8')
const js = fs.readFileSync(path.join(renderer, 'app.js'), 'utf8')
const ids = new Set([...html.matchAll(/id="([\w-]+)"/g)].map((m) => m[1]))
const used = new Set([...js.matchAll(/el\('([\w-]+)'\)/g)].map((m) => m[1]))
const missingIds = [...used].filter((id) => !ids.has(id))
if (missingIds.length > 0) fail(`renderer references missing element ids: ${missingIds.join(', ')}`)

// IPC channels the preload invokes are handled by the main process.
const mainSource = fs.readFileSync(path.join(main, 'main.js'), 'utf8')
const preloadSource = fs.readFileSync(preload, 'utf8')
const handled = new Set([...mainSource.matchAll(/ipcMain\.handle\('([^']+)'/g)].map((m) => m[1]))
const invoked = new Set([...preloadSource.matchAll(/ipcRenderer\.invoke\('([^']+)'/g)].map((m) => m[1]))
const orphans = [...invoked].filter((channel) => !handled.has(channel))
if (orphans.length > 0) fail(`invoked but not handled: ${orphans.join(', ')}`)
const unused = [...handled].filter((channel) => !invoked.has(channel))
if (unused.length > 0) fail(`handled but never invoked: ${unused.join(', ')}`)

// The board preload speaks the fire-and-forget half of IPC: channels it sends
// must have an ipcMain.on listener, and every listener must have a sender.
const boardSource = fs.readFileSync(boardPreload, 'utf8')
const listened = new Set([...mainSource.matchAll(/ipcMain\.on\('([^']+)'/g)].map((m) => m[1]))
const sent = new Set([...boardSource.matchAll(/ipcRenderer\.send(?:Sync)?\('([^']+)'/g)].map((m) => m[1]))
const unheard = [...sent].filter((channel) => !listened.has(channel))
if (unheard.length > 0) fail(`sent but not listened for: ${unheard.join(', ')}`)
const silent = [...listened].filter((channel) => !sent.has(channel))
if (silent.length > 0) fail(`listened for but never sent: ${silent.join(', ')}`)

// The same pairing the other way round: what the main process pushes at the
// shell page has to be listened for in the preload, and a listener with nothing
// sending it is a feature that quietly never arrives. Only the chrome view's
// channels are counted here — a board view is a separate page with its own
// preload, so `board:align` is not this file's business.
const pushed = new Set([
  ...mainSource.matchAll(/toChrome\('([^']+)'/g),
  ...mainSource.matchAll(/chromeView\??\.webContents\.send\('([^']+)'/g)
].map((m) => m[1]))
const awaited = new Set([...preloadSource.matchAll(/ipcRenderer\.on\('([^']+)'/g)].map((m) => m[1]))
const ignored = [...pushed].filter((channel) => !awaited.has(channel))
if (ignored.length > 0) fail(`sent to the shell but not listened for: ${ignored.join(', ')}`)
const expected = [...awaited].filter((channel) => !pushed.has(channel))
if (expected.length > 0) fail(`listened for in the shell but never sent: ${expected.join(', ')}`)

// A closed window lets go of its views. Nothing else can catch this going
// missing: every check above and every test still passes without it, because
// the damage is done to the *next* window — one built by the dock on macOS,
// inheriting a destroyed window's board views and laying them out. The handler
// is asserted here for the same reason the channels are, that main.js cannot be
// loaded to be asked.
if (!/\.(?:once|on)\('closed'[\s\S]{0,600}?lifecycle\.releaseClosedWindow/.test(mainSource)) {
  fail("src/main/main.js has no 'closed' handler calling lifecycle.releaseClosedWindow: " +
    "a reopened window would inherit the closed window's board views")
}

// No ipcMain.on listener is an async function. Nothing awaits one, so a
// rejection inside it is an unhandled rejection in the main process and the
// click that caused it looks like it did nothing — which is what `board:scheme`
// did with a failed theme save. The fix is the shape the sidebar chord uses: a
// synchronous listener over a named async function it catches.
const asyncListeners = [...mainSource.matchAll(/ipcMain\.on\('([^']+)',\s*async\b/g)]
  .map((m) => m[1])
if (asyncListeners.length > 0) {
  fail('ipcMain.on listeners are async functions, so nothing handles a rejection ' +
    `inside them: ${asyncListeners.join(', ')}`)
}

// The sidebar's two widths are one measurement written in two files: main.js
// positions every board view at the current width, styles.css draws the sidebar
// at it. Neither file refers to the other, so changing one alone leaves the
// boards overlapping the sidebar or short of it — and in the rail the sidebar
// ends up under a board, where the project list cannot be clicked at all.
const stylesheet = fs.readFileSync(path.join(renderer, 'styles.css'), 'utf8')
const widths = [
  ['SIDEBAR_WIDTH', '#sidebar'],
  ['RAIL_WIDTH', ':root.sidebar-collapsed #sidebar']
]
for (const [name, selector] of widths) {
  const declared = mainSource.match(new RegExp(`const ${name} = (\\d+)`))
  const rule = stylesheet.match(new RegExp(`^${selector.replace(/[.:#]/g, '\\$&')}\\s*\\{([^}]*)\\}`, 'm'))
  if (!declared) {
    fail(`src/main/main.js no longer declares ${name}`)
    continue
  }
  if (!rule) {
    fail(`src/renderer/styles.css has no ${selector} rule to match ${name}`)
    continue
  }
  // Every declaration that gives the sidebar a width, not just one of them: the
  // rule sets both a width and a flex basis, and one of the two agreeing while
  // the other does not is exactly the half-done edit this is here to catch. The
  // border is a length in the same rule and is none of this check's business.
  const lengths = []
  for (const declaration of rule[1].split(';')) {
    const [, property, value] = declaration.match(/\s*([\w-]+)\s*:([\s\S]*)/) ?? []
    if (!/^(?:min-|max-)?width$|^flex(?:-basis)?$/.test(property ?? '')) continue
    for (const length of value.matchAll(/(\d+)px/g)) lengths.push(length[1])
  }
  if (lengths.length === 0 || lengths.some((value) => value !== declared[1])) {
    fail(`${name} is ${declared[1]} in src/main/main.js but the ${selector} rule in ` +
      `src/renderer/styles.css is ${lengths.length === 0 ? 'set in no px at all' : lengths.map((value) => `${value}px`).join(', ')}`)
  }
}

if (failures > 0) {
  console.error(`\n${failures} check(s) failed`)
  process.exit(1)
}
console.log(`${sources.length} files parse, ${used.size} element ids exist, ` +
  `${invoked.size + sent.size + pushed.size} channels line up, ` +
  `${listened.size} fire-and-forget listeners are synchronous, ` +
  `${widths.length} sidebar widths match the stylesheet`)
