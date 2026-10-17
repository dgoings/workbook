'use strict'

// A small CSS cascade for tests that need to know how the real stylesheet
// treats one element: which rules match it, which declaration wins, and what
// that leaves for the few properties that decide whether the pointer can reach
// it. A string match on one selector misses every other selector that reaches
// the same element; this evaluates them all.
//
// It is not a browser. It reads the selectors and at-rules styles.css uses,
// and throws on anything it cannot evaluate rather than guessing, so a new
// construct fails loudly here instead of passing unread. It does no layout:
// lengths are resolved only in px, rem and em.

// --- the stylesheet -----------------------------------------------------------

// Rules in source order, each with the media conditions it sits under. A media
// condition is not interpreted: each distinct one is a switch that conditions()
// lists, so a test can run every combination of them.
function parseStylesheet (source) {
  const css = source.replace(/\r\n/g, '\n').replace(/\/\*[\s\S]*?\*\//g, '')
  const rules = []
  let i = 0

  // A stray } at the top level would end the walk early and drop every rule
  // after it without a word, so it throws instead.
  function block (media) {
    while (i < css.length) {
      const open = css.indexOf('{', i)
      const close = css.indexOf('}', i)
      if (close !== -1 && (open === -1 || close < open)) {
        if (media.length === 0) throw new Error(`an unmatched } at offset ${close} of the stylesheet`)
        i = close + 1
        return
      }
      if (open === -1) return
      const prelude = css.slice(i, open).trim().replace(/\s+/g, ' ')
      i = open + 1
      if (prelude.startsWith('@media')) {
        block([...media, prelude.slice('@media'.length).trim()])
      } else if (prelude.startsWith('@')) {
        throw new Error(`the cascade cannot read the at-rule ${prelude}`)
      } else {
        const end = css.indexOf('}', i)
        rules.push({
          selectors: splitTopLevel(prelude, ',').map((selector) => selector.trim()),
          declarations: parseDeclarations(css.slice(i, end)),
          media,
          order: rules.length
        })
        i = end + 1
      }
    }
  }

  block([])
  return rules
}

function parseDeclarations (body) {
  const declarations = []
  for (const part of splitTopLevel(body, ';')) {
    const colon = part.indexOf(':')
    if (colon === -1) continue
    const property = part.slice(0, colon).trim().toLowerCase()
    let value = part.slice(colon + 1).trim()
    const important = /!\s*important$/i.test(value)
    if (important) value = value.replace(/!\s*important$/i, '').trim()
    declarations.push({ property, value, important })
  }
  return declarations
}

// Splits on `separator` outside parentheses, brackets and quotes.
function splitTopLevel (text, separator) {
  const parts = []
  let depth = 0
  let quote = null
  let start = 0
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i]
    if (quote) {
      if (c === quote) quote = null
    } else if (c === '"' || c === "'") {
      quote = c
    } else if (c === '(' || c === '[') {
      depth += 1
    } else if (c === ')' || c === ']') {
      depth -= 1
    } else if (c === separator && depth === 0) {
      parts.push(text.slice(start, i))
      start = i + 1
    }
  }
  parts.push(text.slice(start))
  return parts.filter((part) => part.trim() !== '')
}

// Every distinct media condition, for a test to switch on and off.
function conditions (rules) {
  return [...new Set(rules.flatMap((rule) => rule.media))]
}

// Every assignment of true and false to `names`, as objects.
function combinations (names) {
  let all = [{}]
  for (const name of names) {
    all = all.flatMap((env) => [{ ...env, [name]: false }, { ...env, [name]: true }])
  }
  return all
}

// --- the document -------------------------------------------------------------

// The node shape the matcher reads. Anything with tagName, id, class and
// attributes can be adapted to it.
function node (tag, { id = '', classes = [], attributes = {}, text = '' } = {}) {
  return {
    tag: tag.toLowerCase(),
    id,
    classes: new Set(classes),
    attributes: new Map(Object.entries(attributes)),
    text,
    parent: null,
    children: []
  }
}

function append (parent, ...children) {
  for (const child of children) {
    child.parent = parent
    parent.children.push(child)
  }
  return parent
}

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr'])

// The element tree of an HTML file, from its <html> element down. Enough for
// a page written by hand, as index.html is: no implied end tags beyond the void
// elements, and no markup inside <script> or <style>.
function parseHTML (source) {
  const html = source.replace(/\r\n/g, '\n').replace(/<!--[\s\S]*?-->/g, '').replace(/<!doctype[^>]*>/i, '')
  const document = node('#document')
  let current = document
  for (const match of html.matchAll(/<(\/?)([a-zA-Z][\w-]*)((?:[^>"']|"[^"]*"|'[^']*')*)>|([^<]+)/g)) {
    const [, closing, tagName, rest, text] = match
    if (text !== undefined) {
      if (text.trim() !== '') current.text += text.trim()
      continue
    }
    const tag = tagName.toLowerCase()
    if (closing) {
      if (current.tag !== tag) throw new Error(`</${tag}> closes <${current.tag}> in the page`)
      current = current.parent
      continue
    }
    const attributes = {}
    for (const attr of rest.replace(/\/\s*$/, '').matchAll(/([^\s=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s]+)))?/g)) {
      attributes[attr[1].toLowerCase()] = attr[2] ?? attr[3] ?? attr[4] ?? ''
    }
    const element = node(tag, {
      id: attributes.id ?? '',
      classes: (attributes.class ?? '').split(/\s+/).filter(Boolean),
      attributes
    })
    append(current, element)
    if (!VOID.has(tag) && !/\/\s*$/.test(rest)) current = element
  }
  if (current !== document) throw new Error(`<${current.tag}> is never closed in the page`)
  const root = document.children.find((child) => child.tag === 'html')
  if (!root) throw new Error('the page has no <html> element')
  root.parent = null
  return root
}

function byId (root, id) {
  if (root.id === id) return root
  for (const child of root.children) {
    const found = byId(child, id)
    if (found) return found
  }
  return null
}

function ancestors (element) {
  const chain = []
  for (let at = element; at; at = at.parent) chain.unshift(at)
  return chain
}

// --- selectors ----------------------------------------------------------------

// A complex selector as compounds and the combinators between them:
// [{ simple: [...], combinator: null | ' ' | '>' | '~' | '+' }, ...].
// Read once per selector text and kept: a test that runs the cascade over a
// whole page asks for the same few hundred selectors many thousands of times,
// and nothing here changes what it reads.
const parsedSelectors = new Map()

function parseSelector (selector) {
  if (!parsedSelectors.has(selector)) parsedSelectors.set(selector, readSelector(selector))
  return parsedSelectors.get(selector)
}

function readSelector (selector) {
  const compounds = []
  let simple = []
  let combinator = null
  let i = 0
  const text = selector.trim()

  function finish (next) {
    if (simple.length === 0) throw new Error(`the cascade cannot read ${selector}`)
    compounds.push({ simple, combinator })
    simple = []
    combinator = next
  }

  while (i < text.length) {
    const c = text[i]
    if (/\s|[>~+]/.test(c)) {
      let j = i
      let found = ' '
      while (j < text.length && /\s|[>~+]/.test(text[j])) {
        if (text[j] !== ' ' && !/\s/.test(text[j])) found = text[j]
        j += 1
      }
      finish(found)
      i = j
      continue
    }
    if (c === '*') {
      simple.push({ kind: 'universal' })
      i += 1
    } else if (c === '#' || c === '.') {
      const name = text.slice(i + 1).match(/^[\w-]+/)[0]
      simple.push({ kind: c === '#' ? 'id' : 'class', name })
      i += 1 + name.length
    } else if (c === '[') {
      const end = closing(text, i, '[', ']')
      const inner = text.slice(i + 1, end).trim()
      const attr = inner.match(/^([\w-]+)\s*(?:([~|^$*]?=)\s*(?:"([^"]*)"|'([^']*)'|([^\s\]]+)))?$/)
      if (!attr) throw new Error(`the cascade cannot read [${inner}] in ${selector}`)
      simple.push({ kind: 'attribute', name: attr[1].toLowerCase(), op: attr[2] ?? null, value: attr[3] ?? attr[4] ?? attr[5] ?? null })
      i = end + 1
    } else if (c === ':') {
      const element = text[i + 1] === ':'
      const start = i + (element ? 2 : 1)
      const name = text.slice(start).match(/^[\w-]+/)[0].toLowerCase()
      let j = start + name.length
      let args = null
      if (text[j] === '(') {
        const end = closing(text, j, '(', ')')
        args = text.slice(j + 1, end)
        j = end + 1
      }
      const legacy = ['before', 'after', 'first-line', 'first-letter'].includes(name)
      simple.push({ kind: element || legacy ? 'pseudo-element' : 'pseudo-class', name, args })
      i = j
    } else if (/[a-zA-Z]/.test(c)) {
      const name = text.slice(i).match(/^[\w-]+/)[0]
      simple.push({ kind: 'type', name: name.toLowerCase() })
      i += name.length
    } else {
      throw new Error(`the cascade cannot read ${selector}`)
    }
  }
  finish(null)
  return compounds
}

function closing (text, from, open, close) {
  let depth = 0
  for (let i = from; i < text.length; i += 1) {
    if (text[i] === open) depth += 1
    if (text[i] === close) {
      depth -= 1
      if (depth === 0) return i
    }
  }
  throw new Error(`unbalanced ${open} in ${text}`)
}

// [ids, classes, types], the order specificity compares in.
function specificity (selector) {
  const total = [0, 0, 0]
  for (const { simple } of parseSelector(selector)) {
    for (const part of simple) {
      if (part.kind === 'id') total[0] += 1
      else if (part.kind === 'class' || part.kind === 'attribute') total[1] += 1
      else if (part.kind === 'type' || part.kind === 'pseudo-element') total[2] += 1
      else if (part.kind === 'pseudo-class') {
        if (part.name === 'where') continue
        if (['not', 'is', 'has'].includes(part.name)) {
          const inner = splitTopLevel(part.args, ',').map(specificity)
          const max = inner.reduce((best, s) => compare(s, best) > 0 ? s : best, [0, 0, 0])
          for (let k = 0; k < 3; k += 1) total[k] += max[k]
        } else {
          total[1] += 1
        }
      }
    }
  }
  return total
}

function compare (a, b) {
  for (let k = 0; k < a.length; k += 1) if (a[k] !== b[k]) return a[k] - b[k]
  return 0
}

// The interaction state a match is made in: which nodes are hovered and which
// one has the focus. Hovering or focusing an element puts its ancestors under
// the pointer and the focus too, as a browser does.
function interaction ({ hover = null, focus = null } = {}) {
  return {
    hovered: new Set(hover ? ancestors(hover) : []),
    focused: focus,
    focusWithin: new Set(focus ? ancestors(focus) : [])
  }
}

const REST = interaction()

// Whether `selector` selects `element` itself. A selector that ends in a
// pseudo-element styles that pseudo-element, not the element, so it does not.
function matches (selector, element, state = REST) {
  const compounds = parseSelector(selector)
  const last = compounds[compounds.length - 1]
  if (last.simple.some((part) => part.kind === 'pseudo-element')) return false
  if (compounds.some((c, k) => k < compounds.length - 1 && c.simple.some((part) => part.kind === 'pseudo-element'))) {
    throw new Error(`the cascade cannot read ${selector}`)
  }
  return matchFrom(compounds, compounds.length - 1, element, state, selector)
}

function matchFrom (compounds, index, element, state, selector) {
  if (!matchCompound(compounds[index].simple, element, state, selector)) return false
  if (index === 0) return true
  const combinator = compounds[index].combinator
  const siblings = element.parent ? element.parent.children : []
  const position = siblings.indexOf(element)
  switch (combinator) {
    case ' ':
      for (let at = element.parent; at; at = at.parent) {
        if (matchFrom(compounds, index - 1, at, state, selector)) return true
      }
      return false
    case '>':
      return element.parent !== null && matchFrom(compounds, index - 1, element.parent, state, selector)
    case '+':
      return position > 0 && matchFrom(compounds, index - 1, siblings[position - 1], state, selector)
    case '~':
      return siblings.slice(0, Math.max(position, 0)).some((sibling) => matchFrom(compounds, index - 1, sibling, state, selector))
    default:
      throw new Error(`the cascade cannot read the combinator in ${selector}`)
  }
}

// Every part of a compound is decided before an unreadable one may throw: a
// selector for some other element fails on its class, never on syntax this
// cannot evaluate.
function matchCompound (simple, element, state, selector) {
  let unreadable = null
  for (const part of simple) {
    const result = matchSimple(part, element, state, selector)
    if (result === false) return false
    if (result === null) unreadable = part
  }
  if (unreadable) {
    throw new Error(`the cascade cannot evaluate :${unreadable.name} in ${selector}`)
  }
  return true
}

const FORM_CONTROLS = new Set(['button', 'input', 'select', 'textarea', 'fieldset', 'optgroup', 'option'])

// true or false, or null when this cannot say.
function matchSimple (part, element, state, selector) {
  switch (part.kind) {
    case 'universal': return true
    case 'type': return element.tag === part.name
    case 'id': return element.id === part.name
    case 'class': return element.classes.has(part.name)
    case 'attribute': {
      if (!element.attributes.has(part.name)) return false
      const value = element.attributes.get(part.name)
      if (part.op === null) return true
      if (part.op === '=') return value === part.value
      if (part.op === '~=') return value.split(/\s+/).includes(part.value)
      return null
    }
    case 'pseudo-element': return false
    case 'pseudo-class': {
      const siblings = element.parent ? element.parent.children : [element]
      const list = () => splitTopLevel(part.args, ',')
      switch (part.name) {
        case 'root': return element.parent === null
        case 'hover':
        case 'active': return state.hovered.has(element)
        case 'focus':
        case 'focus-visible': return state.focused === element
        case 'focus-within': return state.focusWithin.has(element)
        case 'first-child': return siblings[0] === element
        case 'last-child': return siblings[siblings.length - 1] === element
        case 'only-child': return siblings.length === 1
        case 'empty': return element.children.length === 0 && element.text === ''
        case 'enabled':
        case 'disabled': {
          if (!FORM_CONTROLS.has(element.tag)) return false
          return element.attributes.has('disabled') === (part.name === 'disabled')
        }
        case 'not': return !list().some((inner) => matches(inner, element, state))
        case 'is':
        case 'where': return list().some((inner) => matches(inner, element, state))
        default: return null
      }
    }
    default: return null
  }
}

// --- the cascade --------------------------------------------------------------

// Longhands a test may ask for, under every name that can set them.
const ALIASES = {
  height: ['height', 'block-size'],
  width: ['width', 'inline-size'],
  'min-height': ['min-height', 'min-block-size'],
  'min-width': ['min-width', 'min-inline-size'],
  'font-size': ['font-size', 'font']
}

const INHERITED = new Set(['visibility', 'pointer-events', 'cursor', 'font-size'])

// The declared value that wins for `property` on `element`, or null.
function cascaded (rules, element, property, { env = {}, state = REST } = {}) {
  const names = ALIASES[property] ?? [property]
  let best = null
  for (const rule of rules) {
    if (!rule.media.every((condition) => env[condition] === true)) continue
    const matching = rule.selectors.filter((selector) => matches(selector, element, state))
    if (matching.length === 0) continue
    const weight = matching.map(specificity).reduce((a, b) => compare(a, b) >= 0 ? a : b)
    for (const declaration of rule.declarations) {
      if (!names.includes(declaration.property)) continue
      const key = [declaration.important ? 1 : 0, ...weight, rule.order]
      if (best === null || compare(key, best.key) >= 0) {
        best = { key, declaration, selector: matching[0] }
      }
    }
  }
  return best
}

// The computed value of `property`, as the stylesheet leaves it: keywords
// resolved against the parent, lengths left as written except font-size,
// which comes back in px because other lengths are relative to it.
//
// options.memo, when given, is a Map the caller makes for one stylesheet, one
// page as it stands and one env and state, and drops when any of those
// change: every value worked out is kept in it, parents' included, so a test
// that asks about every node of a page asks the stylesheet once per node.
function computed (rules, element, property, options = {}) {
  const memo = options.memo
  if (!memo) return compute(rules, element, property, options)
  if (!memo.has(element)) memo.set(element, new Map())
  const known = memo.get(element)
  if (!known.has(property)) known.set(property, compute(rules, element, property, options))
  return known.get(property)
}

function compute (rules, element, property, options) {
  const win = cascaded(rules, element, property, options)
  let value = win ? win.declaration.value : null
  if (win && win.declaration.property === 'font') {
    if (value !== 'inherit') throw new Error(`the cascade cannot read font: ${value} (${win.selector})`)
  }
  if (value !== null && value.includes('var(')) {
    throw new Error(`the cascade cannot resolve ${property}: ${value} (${win.selector})`)
  }
  if (value === null || value === 'unset') value = INHERITED.has(property) ? 'inherit' : 'initial'
  if (value === 'inherit') {
    return element.parent ? computed(rules, element.parent, property, options) : initial(element, property, rules, options)
  }
  if (value === 'initial') return initial(element, property, rules, options)
  if (property === 'font-size') return fontSize(rules, element, value, options)
  return value
}

// Initial values, with the user agent's own defaults where they differ and
// matter: a form control takes 13.333px type from Chromium's UA stylesheet
// rather than inheriting.
function initial (element, property, rules, options) {
  switch (property) {
    case 'display': return element.tag === 'span' || element.tag === 'button' ? 'inline' : 'block'
    case 'visibility': return 'visible'
    case 'opacity': return '1'
    case 'pointer-events': return 'auto'
    case 'cursor': return 'auto'
    case 'height':
    case 'width':
    case 'min-height':
    case 'min-width': return 'auto'
    case 'font-size':
      if (FORM_CONTROLS.has(element.tag)) return 13.333
      return element.parent ? computed(rules, element.parent, 'font-size', options) : 16
    default: throw new Error(`no initial value for ${property}`)
  }
}

function fontSize (rules, element, value, options) {
  const parent = element.parent ? computed(rules, element.parent, 'font-size', options) : 16
  const root = rootOf(element)
  const rem = root === element ? 16 : computed(rules, root, 'font-size', options)
  const length = value.match(/^(-?\d*\.?\d+)(px|rem|em|%)$/)
  if (!length) throw new Error(`the cascade cannot resolve font-size: ${value}`)
  const n = Number(length[1])
  return length[2] === 'px' ? n : length[2] === 'rem' ? n * rem : length[2] === 'em' ? n * parent : n * parent / 100
}

function rootOf (element) {
  let at = element
  while (at.parent) at = at.parent
  return at
}

// A length property in px. auto and none are 0 here: as a minimum they ask for
// no space at all.
function px (rules, element, property, options = {}) {
  const value = computed(rules, element, property, options)
  if (value === 'auto' || value === 'none' || value === '0') return 0
  const length = value.match(/^(-?\d*\.?\d+)(px|rem|em)$/)
  if (!length) throw new Error(`the cascade cannot resolve ${property}: ${value}`)
  const n = Number(length[1])
  if (length[2] === 'px') return n
  if (length[2] === 'rem') return n * computed(rules, rootOf(element), 'font-size', options)
  return n * computed(rules, element, 'font-size', options)
}

// The least `axis` ('height' or 'width') the element's box can have, in px: the
// larger of its minimum and its fixed size when that is a length. An auto size
// claims nothing, and a percentage is refused like any length the cascade
// cannot resolve. Padding and box-sizing only add to this; a max-height or
// max-width that caps the fixed size is not read.
function leastSize (rules, element, axis, options = {}) {
  return Math.max(px(rules, element, `min-${axis}`, options), px(rules, element, axis, options))
}

// Why the pointer cannot reach `element`, or null if nothing in the stylesheet
// stops it: no box (display none or contents, here or on an ancestor),
// invisible (visibility, or opacity 0 anywhere above it), or ignoring the
// pointer.
function unreachable (rules, element, options = {}) {
  for (const at of ancestors(element)) {
    const display = computed(rules, at, 'display', options)
    const label = describe(at)
    if (display === 'none') return `${label} is display: none (${cascaded(rules, at, 'display', options).selector})`
    if (display === 'contents' && at === element) return `${label} is display: contents, so it has no box`
    const opacity = computed(rules, at, 'opacity', options)
    if (Number(opacity) === 0 || opacity === '0%') return `${label} is opacity: 0 (${cascaded(rules, at, 'opacity', options).selector})`
  }
  const visibility = computed(rules, element, 'visibility', options)
  if (visibility !== 'visible') return `${describe(element)} is visibility: ${visibility}`
  const events = computed(rules, element, 'pointer-events', options)
  if (events === 'none') return `${describe(element)} is pointer-events: none`
  return null
}

function describe (element) {
  return element.tag + (element.id ? `#${element.id}` : '') + [...element.classes].map((name) => `.${name}`).join('')
}

module.exports = {
  parseStylesheet,
  conditions,
  combinations,
  parseHTML,
  node,
  append,
  byId,
  ancestors,
  interaction,
  matches,
  specificity,
  cascaded,
  computed,
  px,
  leastSize,
  unreachable,
  describe
}
