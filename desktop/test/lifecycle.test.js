'use strict'

// lifecycle.js holds the two decisions main.js's lifecycle paths make, and it
// holds them here precisely so they can be exercised: main.js needs Electron
// and so is testable only through scripts/check-shell.js's parse check. Every
// view below is a hand-written object with the two methods the module actually
// calls, which is why nothing in this file loads Electron and why a case can
// arrange a view whose close throws, or whose contents have already gone —
// neither of which a real WebContentsView can be asked for on demand.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const lifecycle = require('../src/main/lifecycle')

/**
 * A stand-in for a WebContentsView, counting what was asked of it.
 *
 * `destroyed` is the view whose contents have already gone, which a window
 * close may or may not have done for us by then, and `throws` is the one whose
 * close fails — the case the module exists to survive. Being right either way
 * is the point of covering both.
 */
function fakeView ({ destroyed = false, throws = null } = {}) {
  const view = {
    closes: 0,
    webContents: {
      close () {
        view.closes += 1
        if (throws) throw throws
      },
      isDestroyed () {
        return destroyed
      }
    }
  }
  return view
}

describe('releaseClosedWindow', () => {
  test('a window that held nothing releases nothing and reports nothing', () => {
    const boardViews = new Map()

    const { dropped, errors } = lifecycle.releaseClosedWindow({ boardViews, chromeView: null })

    assert.deepEqual(dropped, [])
    assert.deepEqual(errors, [])
  })

  test('every board view and the chrome view are closed, and the map is emptied', () => {
    const first = fakeView()
    const second = fakeView()
    const chromeView = fakeView()
    const boardViews = new Map([['project-a', first], ['project-b', second]])

    const { dropped, errors } = lifecycle.releaseClosedWindow({ boardViews, chromeView })

    assert.deepEqual(dropped, ['project-a', 'project-b'])
    assert.deepEqual(errors, [])
    assert.equal(first.closes, 1)
    assert.equal(second.closes, 1)
    assert.equal(chromeView.closes, 1)
    // The whole point: a new window must not inherit a destroyed window's
    // views, since layout() would then position boards no window holds.
    assert.equal(boardViews.size, 0)
  })

  test('a view whose contents are already destroyed is dropped without being closed', () => {
    const view = fakeView({ destroyed: true })
    const boardViews = new Map([['project-a', view]])

    const { dropped, errors } = lifecycle.releaseClosedWindow({ boardViews, chromeView: null })

    assert.equal(view.closes, 0)
    assert.deepEqual(dropped, ['project-a'])
    assert.deepEqual(errors, [])
    assert.equal(boardViews.size, 0)
  })

  test('a board view whose close throws is reported by project, and the rest still go', () => {
    const angry = fakeView({ throws: new Error('contents are busy') })
    const other = fakeView()
    const chromeView = fakeView()
    const boardViews = new Map([['project-a', angry], ['project-b', other]])

    const { dropped, errors } = lifecycle.releaseClosedWindow({ boardViews, chromeView })

    assert.equal(errors.length, 1)
    assert.match(errors[0], /project-a/)
    assert.match(errors[0], /contents are busy/)
    // A half-emptied map is the bug this is fixing, so one view that throws
    // must not cost the others their close or the map its clear.
    assert.equal(other.closes, 1)
    assert.equal(chromeView.closes, 1)
    assert.deepEqual(dropped, ['project-a', 'project-b'])
    assert.equal(boardViews.size, 0)
  })

  test('a chrome view whose close throws is reported, and the boards are still released', () => {
    const view = fakeView()
    const chromeView = fakeView({ throws: new Error('chrome will not close') })
    const boardViews = new Map([['project-a', view]])

    const { dropped, errors } = lifecycle.releaseClosedWindow({ boardViews, chromeView })

    assert.equal(errors.length, 1)
    assert.match(errors[0], /chrome will not close/)
    assert.equal(view.closes, 1)
    assert.deepEqual(dropped, ['project-a'])
    assert.equal(boardViews.size, 0)
  })

  test('a view with no web contents at all is tolerated', () => {
    // A view whose contents were never created, or a map entry left by a
    // failed open: there is nothing to close and nothing wrong either.
    const boardViews = new Map([['project-a', {}]])

    const { dropped, errors } = lifecycle.releaseClosedWindow({ boardViews, chromeView: {} })

    assert.deepEqual(dropped, ['project-a'])
    assert.deepEqual(errors, [])
    assert.equal(boardViews.size, 0)
  })
})

describe('schemeToTheme', () => {
  test("a board's empty scheme is the shell's 'system'", () => {
    // The one translation: a board spells "follow the system" as '', and the
    // shell has always spelled it 'system'.
    assert.equal(lifecycle.schemeToTheme('', 'dark'), 'system')
  })

  test('light and dark are themselves', () => {
    assert.equal(lifecycle.schemeToTheme('dark', 'system'), 'dark')
    assert.equal(lifecycle.schemeToTheme('light', 'dark'), 'light')
  })

  test('a scheme naming the theme already in force asks for nothing', () => {
    // This is what stops the alignment a board is sent from reporting back:
    // the board that is told and finds itself already there says so, and this
    // is where that report stops.
    assert.equal(lifecycle.schemeToTheme('dark', 'dark'), null)
    assert.equal(lifecycle.schemeToTheme('light', 'light'), null)
    assert.equal(lifecycle.schemeToTheme('', 'system'), null)
  })

  test('anything that is not one of the three themes asks for nothing', () => {
    // The scheme arrives over IPC from a board's page, so it is worth reading
    // strictly: a capitalized spelling is not one of the three either.
    assert.equal(lifecycle.schemeToTheme('Dark', 'system'), null)
    assert.equal(lifecycle.schemeToTheme('sepia', 'system'), null)
    assert.equal(lifecycle.schemeToTheme(undefined, 'system'), null)
    assert.equal(lifecycle.schemeToTheme(null, 'system'), null)
    assert.equal(lifecycle.schemeToTheme(0, 'system'), null)
  })
})
