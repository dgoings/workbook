#!/usr/bin/env node
'use strict'

// Generate the dark overlay for a Workbook board from Workbook's own stylesheet.
//
// The board is light-only and writes most of its palette as literal hex, so a
// dark mode for it is a per-selector override sheet. Enumerating those
// selectors by hand does not work: the first attempt covered the columns and
// missed the whole task-detail route, because a human reading 5000 lines of CSS
// misses things. This reads the source of truth instead and maps every
// declaration that uses a colour we know about.
//
// Re-run after upgrading Workbook:
//   node scripts/generate-board-dark.js
//
// Only colour is touched — never a size, a radius, or a layout — so the worst a
// drift can do is leave a light patch, never a broken board.

const fs = require('node:fs')
const path = require('node:path')

const repo = process.env.WORKBOOK_REPO ||
  path.join(__dirname, '..', '..', '..', 'workbook')
const source = path.join(repo, 'internal', 'webui', 'assets', 'index.html')
const target = path.join(__dirname, '..', 'src', 'renderer', 'board-dark.css')

// Backgrounds: neutral surfaces only. Saturated fills — the red of a delete
// button, the primary of a save button — are left alone, because they are
// already dark enough to carry white text and remapping them would break the
// contrast of the label sitting on top.
const BACKGROUND = {
  '#fff': '#161c26', '#ffffff': '#161c26',
  '#fbfcfe': '#1b222e', '#fafbfd': '#1b222e',
  '#f1f4f8': '#1b222e', '#eef2f8': '#1b222e',
  '#e9eef5': '#0f141c', '#e8edf4': '#0f141c',
  '#fff6e8': '#241d12'
}

// Inks. These are what made labels invisible when they were missed: dark navy
// text keeps its colour on a surface that is now equally dark.
const INK = {
  '#172033': '#e4e9f2', '#34425a': '#c2ccdc', '#4e5d73': '#9aa7bd',
  '#56647a': '#8593ab', '#5f6c85': '#9aa7bd', '#3c4a63': '#c2ccdc',
  '#7c3b08': '#e8c07a', '#b45309': '#e0a45c',
  '#b42318': '#f0806c', '#9c2f25': '#f0806c', '#8f1d1d': '#e08a7c'
}

const BORDER = {
  '#b9c6d8': '#2f3a4a', '#9eafc5': '#3d4b5f', '#e1e7f0': '#232c39',
  '#d5deea': '#262f3d', '#aab8cc': '#3d4b5f', '#a9b7ca': '#3d4b5f',
  '#8496b0': '#3d4b5f', '#cbd5e2': '#2f3a4a', '#e2e8f2': '#232c39'
}

// Corrections, appended after the generated rules so they win at equal
// specificity.
//
// Mapping by colour alone cannot know what a colour *means*. A white fill is
// usually a surface, and mapping it to the dark surface is right — but on a
// switch knob white means "the raised part you can see against the track", and
// mapping both to near-identical darks makes the control disappear. These are
// the places where the role matters more than the value.
const CORRECTIONS = `
/* The switch track is recessed and the knob rides on it, so the two must not
   land on the same colour — which mapping #e9eef5 and #fff separately does. */
.nav-switch__track {
  background: #1b222e !important;
  border-color: #46566d !important;
}

.nav-switch__knob {
  background: #c2ccdc !important;
  box-shadow: 0 1px 2px rgba(0, 0, 0, .6) !important;
}

.nav-switch:hover .nav-switch__track { border-color: var(--wb-primary) !important; }

/* Checked, the track carries the project's primary, which the derived ramp
   lifts to a light colour — so the knob has to darken to stay visible on it. */
.nav-switch[aria-checked="true"] .nav-switch__track {
  background: var(--wb-primary) !important;
  border-color: var(--wb-primary) !important;
}

.nav-switch[aria-checked="true"] .nav-switch__knob {
  background: #0f141c !important;
}

.nav-switch[aria-disabled="true"] .nav-switch__track {
  background: #171d27 !important;
  border-color: #333f4f !important;
}

.nav-switch[aria-disabled="true"] .nav-switch__knob { background: #55627a !important; }
`

const isBackground = (p) => p === 'background' || p === 'background-color'
const isBorder = (p) => p === 'border' || p.startsWith('border-') || p === 'outline' ||
  p === 'outline-color'
const isInk = (p) => p === 'color' || p === 'caret-color' || p === 'fill' || p === 'stroke'

/** Strip @media blocks: their rules are conditional and must stay that way. */
function stripAtRules (css) {
  let out = ''
  for (let i = 0; i < css.length; i += 1) {
    if (css[i] === '@') {
      const brace = css.indexOf('{', i)
      if (brace === -1) break
      let depth = 1
      let j = brace + 1
      while (j < css.length && depth > 0) {
        if (css[j] === '{') depth += 1
        else if (css[j] === '}') depth -= 1
        j += 1
      }
      i = j - 1
      continue
    }
    out += css[i]
  }
  return out
}

function mapDeclaration (property, value) {
  const table = isBackground(property) ? BACKGROUND
    : isBorder(property) ? { ...BORDER, ...BACKGROUND }
    : isInk(property) ? INK
    : null
  if (!table) return null

  let changed = false
  const mapped = value.replace(/#[0-9a-fA-F]{3,8}\b/g, (hex) => {
    const replacement = table[hex.toLowerCase()]
    if (!replacement) return hex
    changed = true
    return replacement
  })
  return changed ? mapped : null
}

function main () {
  if (!fs.existsSync(source)) {
    console.error(`generate-board-dark: cannot find ${source}`)
    console.error('  Set WORKBOOK_REPO=/path/to/workbook')
    process.exit(1)
  }

  const html = fs.readFileSync(source, 'utf8')
  const styles = [...html.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)]
    .map((match) => match[1]).join('\n')
  const css = stripAtRules(styles.replace(/\/\*[\s\S]*?\*\//g, ''))

  const rules = []
  let covered = 0
  for (const match of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = match[1].trim().replace(/\s+/g, ' ')
    if (!selector || selector.startsWith('@') || selector.startsWith(':root')) continue

    const declarations = []
    for (const chunk of match[2].split(';')) {
      const colon = chunk.indexOf(':')
      if (colon === -1) continue
      const property = chunk.slice(0, colon).trim().toLowerCase()
      const value = chunk.slice(colon + 1).trim()
      const mapped = mapDeclaration(property, value)
      if (mapped) declarations.push(`${property}: ${mapped} !important;`)
    }
    if (declarations.length > 0) {
      rules.push(`${selector} {\n  ${declarations.join('\n  ')}\n}`)
      covered += declarations.length
    }
  }

  const header = `/* Dark mode for a Workbook board — GENERATED, do not edit by hand.
 *
 * Regenerate with:  node scripts/generate-board-dark.js
 *
 * The board ships no dark mode: its stylesheet is light-only, with no
 * prefers-color-scheme rule anywhere in it. Workbench injects this sheet into
 * each board's WebContents when it is dark and removes it when it is not.
 *
 * Every declaration carries !important because Electron's insertCSS injects at
 * the *user* origin, which loses to the page's own author rules at equal
 * specificity. Without it the sheet parses and then does nothing.
 *
 * Generated from workbook/internal/webui/assets/index.html by mapping each
 * declaration that uses a known light colour onto a dark counterpart. It is
 * generated rather than written because the hand-written version covered the
 * board columns and silently missed the entire task-detail route.
 *
 * Only colour is mapped — never a size, a radius, or a layout — so the worst a
 * drift can do is leave a light patch, never a broken board.
 *
 * The primary ramp is NOT here: it is derived per project at inject time from
 * the colour that project chose (see src/main/boardtheme.js), so a themed board
 * keeps its own accent instead of being flattened to one blue.
 *
 * ${rules.length} rules, ${covered} declarations.
 */

/* Roots and the page ground, which the generated rules deliberately skip. */
:root {
  --wb-hairline: #262f3d !important;
  --wb-text: #e4e9f2 !important;
  --wb-text-shadow-strong: rgba(0, 0, 0, .55) !important;
  --wb-text-shadow: rgba(0, 0, 0, .45) !important;
  --wb-text-shadow-soft: rgba(0, 0, 0, .35) !important;
  --wb-text-shadow-faint: rgba(0, 0, 0, .3) !important;
  color-scheme: dark !important;
  background: #0f141c !important;
}

body { background: #0f141c !important; }

/* Translucent column grounds resolve against a light page; restate them
   against a dark one. */
.column { background: rgba(22, 28, 38, .36) !important; }
.column--deleted { background: rgba(15, 20, 28, .5) !important; }

/* Form controls render with the platform light theme unless told otherwise. */
input, textarea, select, button { color-scheme: dark !important; }

`

  fs.writeFileSync(target, header + rules.join('\n') + '\n' + CORRECTIONS)
  console.log(`generate-board-dark: ${rules.length} rules, ${covered} declarations -> ${path.relative(process.cwd(), target)}`)
}

main()
