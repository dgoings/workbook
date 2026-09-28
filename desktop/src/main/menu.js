'use strict'

// The application menu, built as data.
//
// Every keyboard shortcut the app offers lives here rather than in a keystroke
// listener, for three reasons: an accelerator fires from the shell page and
// from any board view alike, the menu bar shows the chord beside the action
// so it can be found, and CmdOrCtrl spells the platform difference once.
// The template is plain objects so the bindings can be tested without
// Electron; main.js turns it into a Menu and installs it.

const MAX_PROJECT_ITEMS = 9

/**
 * @param {{ platform: string, projects: Array<{id: string, key: string, name: string}>, activeProjectId: string|null, nextAvailable: boolean, actions: object }} input
 */
function buildMenuTemplate ({ platform, projects, activeProjectId, nextAvailable, actions }) {
  const mac = platform === 'darwin'
  const boardActive = Boolean(activeProjectId)
  // On win32/linux Electron reads a bare & in a menu label as a mnemonic
  // marker and swallows it, so a project named "R&D" would render as "RD"
  // with the D underlined; doubling it escapes the mnemonic. macOS has no
  // mnemonic underlines, so its labels are left exactly as named.
  const escapeLabel = (text) => (mac ? text : String(text).replace(/&/g, '&&'))
  const boardItem = (label, accelerator, command) => ({
    label, accelerator, enabled: boardActive, click: () => actions.boardCommand(command)
  })
  const projectItems = projects.slice(0, MAX_PROJECT_ITEMS).map((project, index) => ({
    label: `${escapeLabel(project.name)} (${escapeLabel(project.key)})`,
    accelerator: `CmdOrCtrl+${index + 1}`,
    click: () => actions.selectProject(index)
  }))

  const template = []
  if (mac) {
    template.push({
      role: 'appMenu',
      submenu: [
        { role: 'about' }, { type: 'separator' }, { role: 'services' }, { type: 'separator' },
        { role: 'hide' }, { role: 'hideOthers' }, { role: 'unhide' }, { type: 'separator' }, { role: 'quit' }
      ]
    })
  }
  template.push({
    label: 'File',
    submenu: [
      boardItem('New Task', 'CmdOrCtrl+N', 'new-task'),
      ...(mac ? [] : [{ type: 'separator' }, { role: 'quit' }])
    ]
  })
  template.push({
    label: 'Edit',
    submenu: [
      { role: 'undo' }, { role: 'redo' }, { type: 'separator' },
      { role: 'cut' }, { role: 'copy' }, { role: 'paste' }, { role: 'selectAll' }
    ]
  })
  template.push({
    label: 'View',
    submenu: [
      { label: 'Toggle Sidebar', accelerator: 'CmdOrCtrl+B', click: () => actions.toggleSidebar() },
      { label: 'Cycle Dark Mode', accelerator: 'CmdOrCtrl+Shift+D', click: () => actions.cycleTheme() },
      { type: 'separator' },
      boardItem('Find on Board', 'CmdOrCtrl+F', 'search'),
      boardItem('Configuration', 'CmdOrCtrl+,', 'config'),
      { type: 'separator' },
      boardItem('Back', 'CmdOrCtrl+[', 'back'),
      boardItem('Forward', 'CmdOrCtrl+]', 'forward'),
      { label: 'Reload Board', accelerator: 'CmdOrCtrl+R', enabled: boardActive, click: () => actions.reloadBoard() }
    ]
  })
  template.push({
    label: 'Go',
    submenu: [
      { label: 'Next', accelerator: 'CmdOrCtrl+0', enabled: nextAvailable, click: () => actions.showNext() },
      { label: 'Import Repositories', accelerator: 'CmdOrCtrl+Shift+I', click: () => actions.showImport() },
      { type: 'separator' },
      { label: 'Previous Project', accelerator: 'CmdOrCtrl+Alt+Up', enabled: projects.length > 0, click: () => actions.stepProject(-1) },
      { label: 'Next Project', accelerator: 'CmdOrCtrl+Alt+Down', enabled: projects.length > 0, click: () => actions.stepProject(1) },
      ...(projectItems.length > 0 ? [{ type: 'separator' }, ...projectItems] : [])
    ]
  })
  template.push({
    role: 'windowMenu',
    submenu: [
      { role: 'minimize' }, { role: 'zoom' }, { role: 'close' },
      ...(mac ? [{ type: 'separator' }, { role: 'front' }] : [])
    ]
  })
  return template
}

module.exports = { buildMenuTemplate, MAX_PROJECT_ITEMS }
