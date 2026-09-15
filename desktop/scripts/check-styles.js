#!/usr/bin/env node
'use strict'

// Check that every class the renderer applies is actually styled.
//
// Four separate edits in this project have removed a CSS rule as collateral
// while replacing an adjacent one, and the result renders — just wrongly, with
// the platform's default button chrome or no layout at all. A missing rule is
// invisible to every other check here, so it gets its own.

const fs = require('node:fs')
const path = require('node:path')

const root = path.join(__dirname, '..', 'src', 'renderer')
const css = fs.readFileSync(path.join(root, 'styles.css'), 'utf8')
const js = fs.readFileSync(path.join(root, 'app.js'), 'utf8')
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8')

// Classes the stylesheet defines, ignoring the contents of comments.
const withoutComments = css.replace(/\/\*[\s\S]*?\*\//g, '')
const defined = new Set([...withoutComments.matchAll(/\.([a-zA-Z][\w-]*)/g)].map((m) => m[1]))

const used = new Set()
for (const match of html.matchAll(/class="([^"]+)"/g)) {
  for (const name of match[1].split(/\s+/)) if (name) used.add(name)
}
for (const match of js.matchAll(/className\s*=\s*['"`]([^'"`]+)['"`]/g)) {
  for (const name of match[1].split(/\s+/)) if (name && !name.includes('$')) used.add(name)
}
for (const match of js.matchAll(/classList\.(?:add|toggle|remove)\('([\w-]+)'/g)) {
  used.add(match[1])
}
// Template-literal class names contribute their static prefix only.
for (const match of js.matchAll(/className\s*=\s*`([^`$]*)/g)) {
  for (const name of match[1].trim().split(/\s+/)) if (name) used.add(name)
}

// Only real class names: a template literal can leave fragments like "??"
// behind, and those are noise rather than findings.
// A selector line ending in a comma must be followed by more selector, not by
// a comment or a blank line: a selector list that never reaches a { swallows
// the next rule, and the page renders with a block quietly missing.
const cssLines = css.split('\n')
const dangling = []
for (let i = 0; i < cssLines.length; i += 1) {
  if (!cssLines[i].trim().endsWith(',')) continue
  if (cssLines[i].trim().startsWith('--') || cssLines[i].includes(':')) continue
  let j = i + 1
  while (j < cssLines.length && cssLines[j].trim() === '') j += 1
  const next = j < cssLines.length ? cssLines[j].trim() : ''
  if (!next || next.startsWith('/*') || next.startsWith('}')) {
    dangling.push(`${i + 1}: ${cssLines[i].trim()}`)
  }
}
if (dangling.length > 0) {
  console.error('selector lists that never reach a rule block:')
  for (const entry of dangling) console.error(`  ${entry}`)
  process.exit(1)
}

const missing = [...used]
  .filter((name) => /^[a-zA-Z][\w-]*$/.test(name))
  .filter((name) => !defined.has(name))
  .sort()
if (missing.length > 0) {
  console.error('classes used by the renderer with no rule in styles.css:')
  for (const name of missing) console.error(`  .${name}`)
  process.exit(1)
}
console.log(`all ${used.size} renderer classes are styled`)
