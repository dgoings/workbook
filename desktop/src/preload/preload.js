'use strict'

// The renderer's whole view of the main process. Nothing here exposes Node or
// the filesystem directly: the renderer names an operation, the main process
// decides whether it is allowed and how it is performed.

const { contextBridge, ipcRenderer } = require('electron')
// This preload is not sandboxed (see the shell view's webPreferences in
// main.js), so it can reach a sibling module, and the title row's height is
// read from the same constant the main process places the window controls by.
const { TITLE_ROW_HEIGHT, RAIL_WIDTH } = require('../main/layout')

contextBridge.exposeInMainWorld('workbench', {
  // 'darwin' | 'win32' | 'linux' — the renderer only uses it for chrome that
  // genuinely differs, not for behaviour.
  platform: process.platform === 'darwin' ? 'mac'
    : process.platform === 'win32' ? 'windows' : 'linux',

  // How tall the window's title row is, in CSS pixels: the row the traffic
  // lights or the overlay controls sit in, which the sidebar head's wordmark
  // and chevron are centered on.
  titleRowHeight: TITLE_ROW_HEIGHT,

  // How wide the collapsed sidebar is, in CSS pixels: the width the main
  // process lays the boards out beside when the sidebar is a rail.
  railWidth: RAIL_WIDTH,

  version: () => ipcRenderer.invoke('workbook:version'),

  // `{ directory }` the one launch on which the CLI was put on the user's PATH,
  // and null on every other one. Asked for during boot rather than pushed: the
  // install races this page's load.
  pathNotice: () => ipcRenderer.invoke('path:notice'),

  listProjects: () => ipcRenderer.invoke('registry:list'),
  loadNext: (limit) => ipcRenderer.invoke('next:load', { limit }),
  pickFolder: () => ipcRenderer.invoke('discovery:pickFolder'),
  scan: (root, maxDepth) => ipcRenderer.invoke('discovery:scan', { root, maxDepth }),
  importRepositories: (selections) => ipcRenderer.invoke('import:apply', { selections }),

  openProject: (projectId, taskId = null) =>
    ipcRenderer.invoke('project:open', { projectId, taskId }),
  showChrome: () => ipcRenderer.invoke('project:showChrome'),
  closeProject: (projectId) => ipcRenderer.invoke('project:close', { projectId }),
  forgetProject: (projectId) => ipcRenderer.invoke('project:forget', { projectId }),

  // The Git identity a board records changes against; see identity:get.
  // `scope` is 'global' or 'local'.
  getIdentity: (projectId) => ipcRenderer.invoke('identity:get', { projectId }),
  setIdentity: (projectId, { name, email, scope }) =>
    ipcRenderer.invoke('identity:set', { projectId, name, email, scope }),

  getTheme: () => ipcRenderer.invoke('theme:get'),

  // The sidebar's collapsed state lives in the main process, which positions the
  // board views against it; the shell asks for it and asks for it to change.
  getSidebar: () => ipcRenderer.invoke('sidebar:get'),
  toggleSidebar: () => ipcRenderer.invoke('sidebar:toggle'),

  onImportProgress: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('import:progress', listener)
    return () => ipcRenderer.removeListener('import:progress', listener)
  },
  onScanProgress: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('discovery:progress', listener)
    return () => ipcRenderer.removeListener('discovery:progress', listener)
  },
  onThemeChanged: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('theme:changed', listener)
    return () => ipcRenderer.removeListener('theme:changed', listener)
  },
  onSidebarChanged: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('sidebar:changed', listener)
    return () => ipcRenderer.removeListener('sidebar:changed', listener)
  },
  // Whether a board view is laid in beside the sidebar: true once one is, false
  // whenever the shell takes the window back.
  onBoardShowing: (handler) => {
    const listener = (_event, showing) => handler(showing)
    ipcRenderer.on('board:showing', listener)
    return () => ipcRenderer.removeListener('board:showing', listener)
  },
  onProjectExited: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('project:exited', listener)
    return () => ipcRenderer.removeListener('project:exited', listener)
  },
  // A menu shortcut that asks for a view or a project. The main process names
  // what was asked; which project or view that is belongs to the renderer,
  // which is the one that holds the list and the current view.
  onShortcut: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('shortcut', listener)
    return () => ipcRenderer.removeListener('shortcut', listener)
  },
  onProjectStarted: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('project:started', listener)
    return () => ipcRenderer.removeListener('project:started', listener)
  }
})
