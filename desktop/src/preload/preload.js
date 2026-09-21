'use strict'

// The renderer's whole view of the main process. Nothing here exposes Node or
// the filesystem directly: the renderer names an operation, the main process
// decides whether it is allowed and how it is performed.

const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('workbench', {
  // 'darwin' | 'win32' | 'linux' — the renderer only uses it for chrome that
  // genuinely differs, not for behaviour.
  platform: process.platform === 'darwin' ? 'mac'
    : process.platform === 'win32' ? 'windows' : 'linux',

  version: () => ipcRenderer.invoke('workbook:version'),

  // `{ directory }` the one launch on which the CLI was put on the user's PATH,
  // and null on every other one. Asked for during boot rather than pushed: the
  // install races this page's load.
  pathNotice: () => ipcRenderer.invoke('path:notice'),

  listProjects: () => ipcRenderer.invoke('registry:list'),
  pickFolder: () => ipcRenderer.invoke('discovery:pickFolder'),
  scan: (root, maxDepth) => ipcRenderer.invoke('discovery:scan', { root, maxDepth }),
  importRepositories: (selections) => ipcRenderer.invoke('import:apply', { selections }),

  openProject: (projectId, taskId = null) =>
    ipcRenderer.invoke('project:open', { projectId, taskId }),
  showChrome: () => ipcRenderer.invoke('project:showChrome'),
  closeProject: (projectId) => ipcRenderer.invoke('project:close', { projectId }),
  forgetProject: (projectId) => ipcRenderer.invoke('project:forget', { projectId }),

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
  onProjectExited: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('project:exited', listener)
    return () => ipcRenderer.removeListener('project:exited', listener)
  }
})
