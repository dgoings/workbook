'use strict'

// What a shortcut does inside a board page.
//
// The board is Workbook's own page, served by `workbook serve`, and its
// controls already know how to make a new task, open the configuration page
// or move through history. A shortcut therefore finds the control and drives
// it — a click on the page's own link runs the page's own router — rather than
// reaching into the page's script, which is not shared with this preload.

function runBoardCommand (command, { document, history }) {
  switch (command) {
    case 'new-task': {
      const link = document.querySelector('a.new-task-link')
      if (!link) return false
      link.click()
      return true
    }
    case 'search': {
      const box = document.querySelector('[data-filter-q]')
      if (!box) return false
      // The row is hidden off the board route, and focusing a hidden control
      // would put the caret nowhere the reader can see.
      const row = typeof box.closest === 'function' ? box.closest('[data-filter-row]') : null
      if (row && row.hidden) return false
      box.focus()
      return true
    }
    case 'config': {
      const link = document.querySelector('a.header-link[href="/config"]')
      if (!link) return false
      link.click()
      return true
    }
    case 'back':
      history.back()
      return true
    case 'forward':
      history.forward()
      return true
    default:
      return false
  }
}

module.exports = { runBoardCommand }
