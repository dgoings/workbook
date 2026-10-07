'use strict'

// Which of the updater's two install paths this copy of Workbench takes, kept
// out of updater.js so it can be tested: updater.js needs Electron, and
// scripts/check-shell.js loads every other module here without it.
//
// No `require` of anything: the caller reads the marker and hands in the
// answer.

/**
 * The file desktop/scripts/after-pack.js writes into the bundle's Resources
 * when electron-builder is about to sign it with an identity. The hook takes
 * the name from here.
 */
const SIGNED_MARKER = 'SIGNED'

/**
 * 'squirrel' to download in the background and install on restart through
 * electron-updater, or 'manual' to download the DMG and open it in Finder.
 *
 * Squirrel.Mac installs an update only over a bundle signed by the same
 * Developer ID as the update, and an ad-hoc bundle is signed by nobody: there
 * `quitAndInstall` returns without doing anything and the user is left
 * believing they upgraded. So a Mac without the marker takes the manual path.
 * Every other platform takes Squirrel's, signed or not.
 *
 * @param {{ platform: string, signed: boolean }} options signed is whether the
 *   marker is in the bundle's Resources.
 * @returns {'squirrel'|'manual'}
 */
function updateFlow ({ platform, signed }) {
  return platform === 'darwin' && !signed ? 'manual' : 'squirrel'
}

module.exports = { SIGNED_MARKER, updateFlow }
