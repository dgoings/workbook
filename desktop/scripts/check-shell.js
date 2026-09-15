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
const sources = [
  ...fs.readdirSync(main).map((name) => path.join(main, name)),
  preload,
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

if (failures > 0) {
  console.error(`\n${failures} check(s) failed`)
  process.exit(1)
}
console.log(`${sources.length} files parse, ${used.size} element ids exist, ${invoked.size} channels line up`)
