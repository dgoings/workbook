'use strict'

// Which install path the updater takes. updater.js needs Electron, so the
// choice lives in updateflow.js and is checked here: a Mac build signed with
// an identity installs in place through Squirrel.Mac, and one that was not
// keeps the DMG download, because Squirrel.Mac would silently refuse it.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { updateFlow } = require('../src/main/updateflow')

describe('updateFlow', () => {
  test('a Mac build with the marker installs in place', () => {
    assert.equal(updateFlow({ platform: 'darwin', signed: true }), 'squirrel')
  })

  test('a Mac build without the marker downloads the DMG', () => {
    assert.equal(updateFlow({ platform: 'darwin', signed: false }), 'manual')
  })

  test('Windows and Linux install in place whether or not a marker exists', () => {
    for (const platform of ['win32', 'linux']) {
      assert.equal(updateFlow({ platform, signed: false }), 'squirrel')
      assert.equal(updateFlow({ platform, signed: true }), 'squirrel')
    }
  })
})
