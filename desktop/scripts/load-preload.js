'use strict'

// Loads a preload the way Electron would, before any page script, with the
// page and Electron stubbed out, so check-shell.js can prove each one at least
// evaluates. Parsing is not enough: on the keyboard-shortcuts branch board.js
// gained a relative require, which parses and which Node would resolve, and
// the sandboxed board view refused it — the whole preload failed to load, the
// board's theme handshake went with it, and every check stayed green.
//
// So a sandboxed preload is given the `require` a sandboxed preload has, not
// Node's: per https://www.electronjs.org/docs/latest/tutorial/sandbox it
// reaches `electron` (the renderer's modules only) and the events, timers and
// url builtins, with or without their node: prefix, and nothing else — no
// sibling file, no fs. An unsandboxed preload gets Node's own require beside
// the file, with only `electron` stubbed. Which preload is which is read from
// main.js, so turning a view's sandbox on or off moves its check with it.

const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const { createRequire } = require('node:module')

const SANDBOXED_BUILTINS = new Set(['events', 'timers', 'url'])

// What require('electron') has in a renderer: the sandbox docs' list. An
// unsandboxed preload's electron also carries clipboard and shell.
const SANDBOXED_ELECTRON = ['contextBridge', 'crashReporter', 'ipcRenderer', 'nativeImage', 'webFrame', 'webUtils']
const UNSANDBOXED_ELECTRON = [...SANDBOXED_ELECTRON, 'clipboard', 'shell']

function toLf (text) {
  return text.replace(/\r\n/g, '\n')
}

/**
 * Every preload main.js gives a view, in order, and whether that view keeps
 * Electron's sandbox — the default, so only an explicit `sandbox: false` turns
 * it off. Each `webPreferences: {` block is read up to its closing brace with
 * comment lines left out, since the board view's comment explains, in so many
 * words, that it is not `sandbox: false`.
 */
function preloadsOf (mainSource) {
  const lines = toLf(mainSource).split('\n')
  const preloads = []
  for (let index = 0; index < lines.length; index++) {
    if (!/\bwebPreferences:\s*\{/.test(lines[index])) continue
    let depth = 0
    const body = []
    for (let at = index; at < lines.length; at++) {
      const line = lines[at].replace(/^\s*\/\/.*$/, '')
      body.push(line)
      for (const char of line) {
        if (char === '{') depth++
        else if (char === '}') depth--
      }
      if (depth <= 0) break
    }
    const text = body.join('\n')
    const preload = text.match(/\bpreload:\s*path\.join\([^)]*'([^'/]+\.js)'\s*\)/)
    if (!preload) continue
    preloads.push({ name: preload[1], sandboxed: !/\bsandbox:\s*false\b/.test(text) })
  }
  return preloads
}

/**
 * The channels main.js pushes at board views: every webContents.send with a
 * literal channel except the shell's own, which go through chromeView.
 */
function sentToBoards (mainSource) {
  return new Set([...toLf(mainSource).matchAll(/(\w+)\??\.webContents\.send\('([^']+)'/g)]
    .filter((m) => m[1] !== 'chromeView')
    .map((m) => m[2]))
}

/** require('electron') as a preload sees it, recording what the preload does with IPC. */
function electronStub (allowed, record) {
  const ipcRenderer = {
    on (channel) { record.listened.add(channel); return ipcRenderer },
    once (channel) { record.listened.add(channel); return ipcRenderer },
    removeListener () { return ipcRenderer },
    removeAllListeners () { return ipcRenderer },
    send (channel) { record.sent.add(channel) },
    // The answer the main process gives the board's theme:current today; a
    // preload only needs some string to carry on with.
    sendSync (channel) { record.sent.add(channel); return 'system' },
    invoke (channel) { record.invoked.add(channel); return Promise.resolve() }
  }
  const modules = {
    ipcRenderer,
    contextBridge: {
      exposeInMainWorld (name, api) { record.exposed.set(name, api) }
    }
  }
  const electron = {}
  for (const name of allowed) electron[name] = modules[name] ?? {}
  // A name outside the list is a module this preload does not have, and
  // destructuring it would quietly give undefined; say so where it happens.
  return new Proxy(electron, {
    get (target, key) {
      if (typeof key !== 'string' || key in target) return target[key]
      throw new Error(`require('electron').${key} is not available in this preload`)
    }
  })
}

/** The page the preload runs before: enough of a document and window to load. */
function pageGlobals () {
  const storage = new Map()
  const root = {
    classList: {
      names: new Set(),
      add (...names) { for (const name of names) this.names.add(name) },
      remove (...names) { for (const name of names) this.names.delete(name) },
      contains (name) { return this.names.has(name) }
    },
    dataset: {}
  }
  return {
    console,
    document: {
      documentElement: root,
      readyState: 'loading',
      addEventListener () {},
      removeEventListener () {},
      querySelector () { return null },
      querySelectorAll () { return [] }
    },
    history: { back () {}, forward () {} },
    localStorage: {
      getItem: (key) => storage.has(key) ? storage.get(key) : null,
      setItem: (key, value) => { storage.set(key, String(value)) },
      removeItem: (key) => { storage.delete(key) }
    },
    matchMedia: () => ({ matches: false, addEventListener () {}, removeEventListener () {} }),
    MutationObserver: class { observe () {} disconnect () {} },
    addEventListener () {},
    removeEventListener () {},
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    queueMicrotask
  }
}

/**
 * Evaluates the preload at `file` and returns what it did: the channels it
 * listened for, sent and invoked at load, and what it exposed to the page.
 * Throws, naming the file as `label`, if it does not load or exposes an
 * undefined value.
 */
function loadPreload (file, { sandboxed, label = file }) {
  const record = { listened: new Set(), sent: new Set(), invoked: new Set(), exposed: new Map() }
  const electron = electronStub(sandboxed ? SANDBOXED_ELECTRON : UNSANDBOXED_ELECTRON, record)
  const nodeRequire = createRequire(file)
  const preloadRequire = (id) => {
    if (id === 'electron') return electron
    if (!sandboxed) return nodeRequire(id)
    const bare = id.startsWith('node:') ? id.slice('node:'.length) : id
    if (SANDBOXED_BUILTINS.has(bare)) return require(`node:${bare}`)
    throw new Error(`require('${id}') is not available in a sandboxed preload, which can ` +
      'require only electron, events, timers and url: inline what it needs instead')
  }

  const context = vm.createContext(pageGlobals())
  const window = vm.runInContext('globalThis', context)
  window.window = window
  // A sandboxed preload's process is a polyfill with little but the platform
  // on it; an unsandboxed one has Node's.
  const preloadProcess = sandboxed
    ? { platform: process.platform, arch: process.arch, type: 'renderer', versions: { ...process.versions }, env: {} }
    : process

  const source = fs.readFileSync(file, 'utf8')
  const module = { exports: {} }
  try {
    const run = vm.compileFunction(source,
      ['exports', 'require', 'module', '__filename', '__dirname', 'process', 'Buffer', 'global', 'setImmediate', 'clearImmediate'],
      { filename: file, parsingContext: context })
    run(module.exports, preloadRequire, module, file, path.dirname(file), preloadProcess, Buffer, window, setImmediate, clearImmediate)
  } catch (error) {
    throw new Error(`${label} does not load as a${sandboxed ? ' sandboxed' : 'n unsandboxed'} preload: ${error.message}`)
  }

  for (const [name, api] of record.exposed) {
    const missing = Object.entries(api ?? {}).filter(([, value]) => value === undefined).map(([key]) => `${name}.${key}`)
    if (missing.length > 0) throw new Error(`${label} exposes undefined: ${missing.join(', ')}`)
  }
  return record
}

module.exports = { preloadsOf, sentToBoards, loadPreload }
