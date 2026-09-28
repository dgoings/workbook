'use strict'

// check-preload-inline.js keeps board.js's inlined copy of runBoardCommand
// equal to its tested source, boardcommand.js. Git for Windows checks this
// repository out with CRLF line endings, so this proves the comparison sees
// the same text — and the same drift — no matter which line ending either
// file arrives with.

const fs = require('node:fs')
const path = require('node:path')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { extractFunction, locateCopy, compare } = require('../scripts/check-preload-inline')

const root = path.join(__dirname, '..')
const sourcePath = path.join(root, 'src', 'preload', 'boardcommand.js')
const targetPath = path.join(root, 'src', 'preload', 'board.js')

function readLf (file) {
  return fs.readFileSync(file, 'utf8').replace(/\r\n/g, '\n')
}

function toCrlf (text) {
  return text.replace(/\n/g, '\r\n')
}

describe('check-preload-inline', () => {
  test('the real files copy verbatim with LF text', () => {
    const sourceText = readLf(sourcePath)
    const targetText = readLf(targetPath)
    const { wanted, found } = compare(sourceText, targetText)
    assert.equal(found, wanted)
  })

  test('a CRLF checkout of both files still copies verbatim', () => {
    const sourceText = toCrlf(readLf(sourcePath))
    const targetText = toCrlf(readLf(targetPath))
    // extractFunction and locateCopy must still find the function and the
    // markers in CRLF text, not just agree once found.
    assert.doesNotThrow(() => extractFunction(sourceText))
    assert.doesNotThrow(() => locateCopy(targetText))
    const { wanted, found } = compare(sourceText, targetText)
    assert.equal(found, wanted)
  })

  test('only one file arriving as CRLF is still verbatim', () => {
    const lfSource = readLf(sourcePath)
    const lfTarget = readLf(targetPath)

    const mixedA = compare(toCrlf(lfSource), lfTarget)
    assert.equal(mixedA.found, mixedA.wanted)

    const mixedB = compare(lfSource, toCrlf(lfTarget))
    assert.equal(mixedB.found, mixedB.wanted)
  })

  test('a real one-character drift is still reported', () => {
    const sourceText = readLf(sourcePath)
    const targetText = readLf(targetPath)
    const { from, to } = locateCopy(targetText)
    // Corrupt a single character inside the copied text, well clear of the
    // markers, so this is drift and not a broken marker pair.
    const drifted = `${targetText.slice(0, from + 1)}x${targetText.slice(from + 2, to)}${targetText.slice(to)}`

    const { wanted, found } = compare(sourceText, drifted)
    assert.notEqual(found, wanted)
  })
})
