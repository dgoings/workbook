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

  listProjects: () => ipcRenderer.invoke('registry:list'),
  pickFolder: () => ipcRenderer.invoke('discovery:pickFolder'),
  scan: (root, maxDepth) => ipcRenderer.invoke('discovery:scan', { root, maxDepth }),
  importRepositories: (selections) => ipcRenderer.invoke('import:apply', { selections }),

  openProject: (projectId, taskId = null) =>
    ipcRenderer.invoke('project:open', { projectId, taskId }),
  listStatuses: (projectId, refresh = false) =>
    ipcRenderer.invoke('project:statuses', { projectId, refresh }),
  setTaskStatus: (projectId, taskId, status) =>
    ipcRenderer.invoke('task:status', { projectId, taskId, status }),
  showChrome: () => ipcRenderer.invoke('project:showChrome'),
  closeProject: (projectId) => ipcRenderer.invoke('project:close', { projectId }),
  forgetProject: (projectId) => ipcRenderer.invoke('project:forget', { projectId }),

  loadQueue: () => ipcRenderer.invoke('queue:load'),

  assignTask: (projectId, taskId, email) =>
    ipcRenderer.invoke('task:assign', { projectId, taskId, email }),
  unassignTask: (projectId, taskId, email) =>
    ipcRenderer.invoke('task:unassign', { projectId, taskId, email }),

  listPeople: (refresh = false) => ipcRenderer.invoke('people:list', { refresh }),
  setDefaultAssignee: (email) => ipcRenderer.invoke('people:setDefault', { email }),
  mergePeople: (emails) => ipcRenderer.invoke('people:merge', { emails }),
  splitPerson: (email) => ipcRenderer.invoke('people:split', { email }),
  renamePerson: (id, displayName) => ipcRenderer.invoke('people:rename', { id, displayName }),

  checkForUpdates: () => ipcRenderer.invoke('update:check'),
  installUpdate: () => ipcRenderer.invoke('update:install'),

  getTheme: () => ipcRenderer.invoke('theme:get'),
  setTheme: (theme) => ipcRenderer.invoke('theme:set', { theme }),

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
  onUpdateAvailable: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('update:available', listener)
    return () => ipcRenderer.removeListener('update:available', listener)
  },
  onThemeChanged: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('theme:changed', listener)
    return () => ipcRenderer.removeListener('theme:changed', listener)
  },
  onProjectExited: (handler) => {
    const listener = (_event, payload) => handler(payload)
    ipcRenderer.on('project:exited', listener)
    return () => ipcRenderer.removeListener('project:exited', listener)
  }
})
