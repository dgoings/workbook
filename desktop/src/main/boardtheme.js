'use strict'

// Deriving a dark primary ramp from whatever colour a project chose.
//
// Workbook lets a project set its own board colour and derives the rest of the
// ramp from it (internal/webui/display.go builds --wb-primary-hover, -edge and
// the tints by scaling the chosen colour). That derivation targets a light
// board. The same idea is applied here for a dark one: the project's hue is
// kept, and only lightness and saturation are moved to what a dark ground
// needs, so a themed board still reads as itself in dark mode instead of being
// flattened to one generic blue.

/** Workbook's own default, used when a board reports no colour of its own. */
const DEFAULT_PRIMARY = '#2457d6'

function parseHex (value) {
  const hex = String(value ?? '').trim().replace(/^#/, '')
  const full = hex.length === 3 ? hex.split('').map((c) => c + c).join('') : hex
  if (!/^[0-9a-fA-F]{6}$/.test(full)) return null
  return [0, 2, 4].map((offset) => parseInt(full.slice(offset, offset + 2), 16) / 255)
}

function toHSL ([r, g, b]) {
  const max = Math.max(r, g, b)
  const min = Math.min(r, g, b)
  const lightness = (max + min) / 2
  const delta = max - min
  if (delta === 0) return [0, 0, lightness]

  const saturation = delta / (1 - Math.abs(2 * lightness - 1))
  let hue
  if (max === r) hue = ((g - b) / delta) % 6
  else if (max === g) hue = (b - r) / delta + 2
  else hue = (r - g) / delta + 4
  return [(hue * 60 + 360) % 360, saturation, lightness]
}

function toHex (hue, saturation, lightness) {
  const s = Math.min(Math.max(saturation, 0), 1)
  const l = Math.min(Math.max(lightness, 0), 1)
  const c = (1 - Math.abs(2 * l - 1)) * s
  const x = c * (1 - Math.abs(((hue / 60) % 2) - 1))
  const m = l - c / 2
  const sector = Math.floor(((hue % 360) + 360) % 360 / 60)
  const rgb = [
    [c, x, 0], [x, c, 0], [0, c, x], [0, x, c], [x, 0, c], [c, 0, x]
  ][sector]
  return '#' + rgb
    .map((channel) => Math.round((channel + m) * 255).toString(16).padStart(2, '0'))
    .join('')
}

/**
 * Build the `:root` block that darkens one board's primary ramp.
 *
 * The chosen hue is kept. Lightness is lifted into the band that stays legible
 * on a dark surface — Workbook's own #2457d6 sits at 49% and fails there — and
 * saturation is bounded so a near-grey choice does not come back as a colour
 * the user never picked.
 *
 * !important throughout for the reason board-dark.css carries it: insertCSS
 * injects at the user origin, which otherwise loses to the page's own rules.
 *
 * @param {string} primary the board's current --wb-primary
 * @returns {string} CSS
 */
function darkPrimaryRamp (primary) {
  const parsed = parseHex(primary) ?? parseHex(DEFAULT_PRIMARY)
  const [hue, saturation] = toHSL(parsed)

  const s = Math.min(Math.max(saturation, 0.35), 0.92)
  const base = toHex(hue, s, 0.68)
  const hover = toHex(hue, s, 0.77)
  const edge = toHex(hue, s, 0.56)
  const ink = toHex(hue, s * 0.55, 0.68)
  const chip = toHex(hue, s * 0.45, 0.24)
  const muted = toHex(hue, s * 0.4, 0.34)
  const tint1 = toHex(hue, s * 0.45, 0.14)
  const tint2 = toHex(hue, s * 0.42, 0.13)
  const tint3 = toHex(hue, s * 0.4, 0.115)
  const tint4 = toHex(hue, s * 0.38, 0.1)

  // A lifted primary is a light fill, so ink on top of it has to be dark.
  return `:root {
  --wb-primary: ${base} !important;
  --wb-primary-hover: ${hover} !important;
  --wb-primary-edge: ${edge} !important;
  --wb-primary-glow: ${toHex(hue, s, 0.68)}2e !important;
  --wb-primary-glow-strong: ${toHex(hue, s, 0.68)}47 !important;
  --wb-primary-tint-1: ${tint1} !important;
  --wb-primary-tint-2: ${tint2} !important;
  --wb-primary-tint-3: ${tint3} !important;
  --wb-primary-tint-4: ${tint4} !important;
  --wb-primary-chip: ${chip} !important;
  --wb-primary-muted: ${muted} !important;
  --wb-primary-ink: ${ink} !important;
}
.save-button, .relationship-editor button { color: #0f141c !important; }
`
}

module.exports = { darkPrimaryRamp, DEFAULT_PRIMARY }
