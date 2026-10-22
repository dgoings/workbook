'use strict'

// The guard behind every "one node under the pointer" test. Native tooltips
// have to survive electron/electron#49843: on macOS, when the pointer moves
// onto a different node that has the same tooltip, Chromium clears the tooltip
// and sets the same text again, and macOS then rarely shows it after the first
// hover. So every titled element is the only node under the pointer for its
// own tooltip: whatever inside it only shows text or decoration ignores the
// pointer, and a control or a span inside it that keeps the pointer carries a
// title of its own with different words. "One node under the pointer" in
// styles.css says why.
//
// The guard runs the real stylesheet's cascade over the real page and the
// elements app.js builds. It is the cascade from cascade.js, not a browser, so
// it reads declarations rather than layout; a full-size overlay would be
// invisible to it, and so would a child moved outside its parent's box. app.js
// runs only in a page, so its functions are taken out of its source the way
// category-toggle.test.js takes them, and run against the small fake DOM here.

const fs = require('node:fs')
const path = require('node:path')
const assert = require('node:assert/strict')

const cascade = require('./cascade')

const src = path.join(__dirname, '..', '..', 'src')

function read (...parts) {
  return fs.readFileSync(path.join(src, ...parts), 'utf8').replace(/\r\n/g, '\n')
}

function code (...parts) {
  return read(...parts)
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

const app = code('renderer', 'app.js')
const page = read('renderer', 'index.html')
const stylesheet = cascade.parseStylesheet(read('renderer', 'styles.css'))

function fn (name) {
  const match = app.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name} in app.js`)
  return match[0]
}

// --- the fake DOM -------------------------------------------------------------

// A text node, which the page holds as text of the element around it.
class FakeText {
  constructor (text) {
    this.textContent = String(text)
    this.parentNode = null
  }
}

class FakeElement {
  constructor (tagName) {
    this.tagName = tagName.toUpperCase()
    this.className = ''
    this.dataset = {}
    this.children = []
    this.parentNode = null
    this.attributes = new Map()
    this.listeners = new Map()
    this.textContent = ''
    this.title = ''
    this.hidden = false
    this.draggable = false
    this.type = ''
    this.value = ''
    this.scrollTop = 0
    const self = this
    this.classList = {
      add (...names) { self.className = [...new Set([...self.className.split(/\s+/).filter(Boolean), ...names])].join(' ') },
      contains (name) { return self.className.split(/\s+/).includes(name) }
    }
  }

  // Only ever cleared: renderNext empties its container this way.
  set innerHTML (value) {
    assert.equal(value, '', 'the fake DOM can only clear innerHTML')
    this.children = []
    this.textContent = ''
  }

  append (...nodes) {
    for (const node of nodes) {
      node.parentNode = this
      this.children.push(node)
    }
  }

  setAttribute (name, value) { this.attributes.set(name, String(value)) }
  getAttribute (name) { return this.attributes.has(name) ? this.attributes.get(name) : null }
  addEventListener (type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, [])
    this.listeners.get(type).push(listener)
  }

  // A tag name or a class, or a list of them: all the handlers here ask.
  closest (selector) {
    const tests = selector.split(',').map((part) => part.trim())
    for (const part of tests) {
      if (!/^\.?[\w-]+$/.test(part)) throw new Error(`the fake DOM cannot match ${part}`)
    }
    for (let at = this; at; at = at.parentNode) {
      if (tests.some((part) => part.startsWith('.') ? at.classList.contains(part.slice(1)) : at.tagName === part.toUpperCase())) return at
    }
    return null
  }

  // Descendants, depth first, that carry `name` among their classes.
  find (name) {
    const found = []
    for (const child of this.children) {
      if (!(child instanceof FakeElement)) continue
      if (child.classList.contains(name)) found.push(child)
      found.push(...child.find(name))
    }
    return found
  }
}

const fakeDocument = {
  createElement: (tag) => new FakeElement(tag),
  createTextNode: (text) => new FakeText(text)
}

// --- the page, as the cascade reads it ---------------------------------------

function adopt (fake) {
  const attributes = Object.fromEntries(fake.attributes)
  if (fake.type) attributes.type = fake.type
  if (fake.title) attributes.title = fake.title
  if (fake.hidden) attributes.hidden = ''
  if (fake.draggable) attributes.draggable = 'true'
  const texts = fake.children.filter((child) => child instanceof FakeText).map((child) => child.textContent)
  const element = cascade.node(fake.tagName, {
    classes: fake.className.split(/\s+/).filter(Boolean),
    attributes,
    text: [fake.textContent, ...texts].join('')
  })
  return cascade.append(element, ...fake.children.filter((child) => child instanceof FakeElement).map(adopt))
}

function walk (node, visit) {
  visit(node)
  for (const child of node.children) walk(child, visit)
}

const CONTROLS = new Set(['button', 'input', 'select', 'textarea', 'a'])

function title (node) {
  return node.attributes.get('title') || null
}

// Whether the element has a box and takes the pointer: no ancestor is display
// none, it is not display: contents itself, and it is neither hidden nor
// ignoring the pointer. Opacity does not count: a transparent element still
// takes the pointer, and so still sits between the pointer and its tooltip.
function boxed (rules, node, options) {
  return cascade.ancestors(node).every((at) => cascade.computed(rules, at, 'display', options) !== 'none')
}

function takesPointer (rules, node, options) {
  return cascade.computed(rules, node, 'visibility', options) === 'visible' &&
    cascade.computed(rules, node, 'pointer-events', options) !== 'none'
}

function hittable (rules, node, options) {
  return boxed(rules, node, options) &&
    cascade.computed(rules, node, 'display', options) !== 'contents' &&
    takesPointer(rules, node, options)
}

// Every place under `root` where the pointer could move onto a different node
// with the same tooltip, as sentences. Text counts: a text node is a node of
// its own to the hit test, so text directly inside a titled element, or inside
// an element that shares its tooltip, is a second node with that tooltip.
function crossings (rules, root, options) {
  const found = []
  function visit (node, around) {
    const own = title(node)
    const tooltip = own ?? around?.title ?? null
    if (tooltip !== null) {
      const where = cascade.describe(node)
      if (around && hittable(rules, node, options)) {
        if (own === null) {
          found.push(`${where} takes the pointer inside ${cascade.describe(around.node)} with no title of its own, so it shares the tooltip "${around.title}"`)
        } else if (own === around.title) {
          found.push(`${where} takes the pointer inside ${cascade.describe(around.node)} and repeats its tooltip "${own}"`)
        }
      }
      if (node.text !== '' && boxed(rules, node, options) && takesPointer(rules, node, options)) {
        found.push(`the text "${node.text}" directly inside ${where} takes the pointer, a second node with the tooltip "${tooltip}"`)
      }
      // Chromium hit-tests an ellipsis as part of the last element it cuts
      // short, even one that ignores the pointer (measured in the app: the
      // "…" of the version line found its span). So an element that takes the
      // pointer must not ellipsize elements of its own under a tooltip; the
      // element that ignores the pointer has to do its own ellipsizing.
      if (node.children.length > 0 && hittable(rules, node, options) &&
          cascade.computed(rules, node, 'text-overflow', options) === 'ellipsis') {
        found.push(`${where} takes the pointer and ellipsizes its children, and its ellipsis is a second node with the tooltip "${tooltip}"`)
      }
    }
    const next = own === null ? around : { node, title: own }
    for (const child of node.children) visit(child, next)
  }
  visit(root, null)
  return found
}

// Every titled element and every control under `root` that is drawn but would
// not take the pointer: a tooltip nobody can hover, or a control nobody can
// press, is the over-correction this fix must not make.
function stranded (rules, root, options) {
  const found = []
  walk(root, (node) => {
    if (title(node) === null && !CONTROLS.has(node.tag)) return
    if (!boxed(rules, node, options)) return
    if (!hittable(rules, node, options)) found.push(`${cascade.describe(node)} is drawn but does not take the pointer`)
  })
  return found
}

// The cascade's options for the page at rest, with a memo of their own: a test
// that changes the page or the stylesheet must not read another's answers.
function rest () {
  return { env: {}, state: cascade.interaction(), memo: new Map() }
}

module.exports = {
  read,
  code,
  app,
  page,
  stylesheet,
  fn,
  FakeElement,
  FakeText,
  fakeDocument,
  adopt,
  walk,
  title,
  crossings,
  stranded,
  rest
}
