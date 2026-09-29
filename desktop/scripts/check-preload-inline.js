#!/usr/bin/env node
'use strict'

// board.js runs in a sandboxed preload, where `require` reaches the electron
// module and a few Node builtins and nothing else — a sibling file cannot be
// required, and a preload that fails to load fails entirely. So runBoardCommand
// is written once in boardcommand.js, where the tests can reach it without
// Electron, and copied into board.js between two markers.
//
// A copy nobody checks is a copy that drifts, and this one would drift
// silently: the tests would still pass against boardcommand.js while the board
// ran the old text. This asserts the two are the same, and with --write it
// makes them so.

const fs = require('node:fs')
const path = require('node:path')

const root = path.join(__dirname, '..')
const source = path.join(root, 'src', 'preload', 'boardcommand.js')
const target = path.join(root, 'src', 'preload', 'board.js')
const BEGIN = '// boardcommand:begin'
const END = '// boardcommand:end'

// Git for Windows checks this repository out with CRLF line endings, so
// reading either file verbatim would see a different text on Windows than on
// macOS or Ubuntu. Both files are read through here so the check sees the
// same LF text on every platform.
function readText (file) {
  return toLf(fs.readFileSync(file, 'utf8'))
}

function toLf (text) {
  return text.replace(/\r\n/g, '\n')
}

/**
 * The text of runBoardCommand in boardcommand.js.
 *
 * Taken by its opening line and the first line that closes it at column zero,
 * rather than by parsing: the function is top-level and the file is this
 * project's own, so the brace that starts a line is its end. Normalized to
 * LF first, so a CRLF checkout still finds a "}" line rather than a "}\r"
 * that never matches.
 */
function extractFunction (sourceText) {
  const lines = toLf(sourceText).split('\n')
  // Only runBoardCommand is copied across, so a second top-level function —
  // a helper pulled out of it, say — would be tested here and missing from the
  // board, where the call to it would throw at the first shortcut. That is
  // exactly the silent drift this script exists to catch, so it is refused
  // rather than copied halfway.
  const declared = lines.filter((line) => line.startsWith('function ')).map((line) => line.slice('function '.length).split(/[\s(]/)[0])
  if (declared.length > 1) {
    fatal(`${rel(source)} declares more than one top-level function (${declared.join(', ')}), ` +
      'and only runBoardCommand is copied into the board preload.\n' +
      'The board would call a helper that is not there. Inline the helper into ' +
      'runBoardCommand, or teach this script and the markers in ' +
      `${rel(target)} to carry both.`)
  }
  const start = lines.findIndex((line) => line.startsWith('function runBoardCommand '))
  if (start < 0) fatal(`${rel(source)} no longer declares a top-level runBoardCommand`)
  const end = lines.findIndex((line, index) => index > start && line === '}')
  if (end < 0) fatal(`${rel(source)}: runBoardCommand has no closing brace at column zero`)
  return lines.slice(start, end + 1).join('\n')
}

/**
 * Where the copy sits in board.js, by its markers.
 *
 * Offsets are into the LF-normalized text, so a caller that slices with them
 * must normalize the same way before slicing (compare does).
 */
function locateCopy (targetText) {
  const text = toLf(targetText)
  const begin = text.indexOf(BEGIN)
  const finish = text.indexOf(END)
  if (begin < 0 || finish < 0 || finish < begin) {
    fatal(`${rel(target)} has no ${BEGIN} … ${END} pair around the copy of runBoardCommand`)
  }
  return { from: begin + BEGIN.length, to: finish }
}

/** Whether target's copy of runBoardCommand still matches source's. */
function compare (sourceText, targetText) {
  const wanted = extractFunction(sourceText)
  const { from, to } = locateCopy(targetText)
  const found = toLf(targetText).slice(from, to).trim()
  return { wanted, found }
}

function rel (file) { return path.relative(root, file) }

function fatal (message) {
  console.error(message)
  process.exit(1)
}

if (require.main === module) {
  const sourceText = readText(source)
  const targetText = readText(target)
  const { wanted, found } = compare(sourceText, targetText)

  if (process.argv.includes('--write')) {
    if (found === wanted) {
      console.log(`${rel(target)} already carries ${rel(source)}'s runBoardCommand`)
    } else {
      const { from, to } = locateCopy(targetText)
      fs.writeFileSync(target, `${targetText.slice(0, from)}\n${wanted}\n${targetText.slice(to)}`)
      console.log(`copied runBoardCommand from ${rel(source)} into ${rel(target)}`)
    }
  } else if (found !== wanted) {
    fatal(`${rel(target)}'s inlined runBoardCommand is not ${rel(source)}'s.\n` +
      'The tests cover boardcommand.js, so the board would be running text nothing tests. ' +
      'Run `npm run sync:boardcommand` to copy it across.')
  } else {
    console.log(`${rel(target)} carries ${rel(source)}'s runBoardCommand verbatim ` +
      `(${wanted.split('\n').length} lines)`)
  }
} else {
  module.exports = { readText, extractFunction, locateCopy, compare }
}
