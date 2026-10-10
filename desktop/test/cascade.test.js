'use strict'

// The cascade in test/support stands in for a browser in the stylesheet
// guards, so it has to get the rules a browser does right, and has to refuse
// what it cannot read rather than read it as "no match".

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const cascade = require('./support/cascade')

function page () {
  const root = cascade.parseHTML(`<!doctype html>
<html class="rail">
<body>
  <!-- a comment with <b>markup</b> -->
  <ul id="list" class="list">
    <li class="row first"><button class="go" type="button">Go</button></li>
    <li class="row"><input type="text" disabled><span class="tag">x</span></li>
  </ul>
</body>
</html>`)
  const list = cascade.byId(root, 'list')
  return { root, list, button: list.children[0].children[0], input: list.children[1].children[0], tag: list.children[1].children[1] }
}

describe('the page', () => {
  test('parses into the element tree, void elements and comments included', () => {
    const { root, list, button, input } = page()
    assert.equal(root.tag, 'html')
    assert.equal(root.parent, null)
    assert.deepEqual(cascade.ancestors(button).map(cascade.describe), ['html.rail', 'body', 'ul#list.list', 'li.row.first', 'button.go'])
    assert.equal(list.children.length, 2)
    assert.equal(input.attributes.get('type'), 'text')
    assert.equal(input.attributes.get('disabled'), '')
    assert.equal(input.children.length, 0)
  })

  test('reads CRLF the same as LF', () => {
    const lf = cascade.parseHTML('<html>\n<body class="a"></body>\n</html>')
    const crlf = cascade.parseHTML('<html>\r\n<body class="a"></body>\r\n</html>')
    assert.deepEqual(cascade.describe(crlf.children[0]), cascade.describe(lf.children[0]))
  })

  test('refuses markup it cannot nest', () => {
    assert.throws(() => cascade.parseHTML('<html><body></div></html>'), /closes <body>/)
  })
})

describe('selectors', () => {
  test('match through each combinator', () => {
    const { button, tag } = page()
    assert.ok(cascade.matches(':root.rail .go', button))
    assert.ok(cascade.matches('.list > .row > button', button))
    assert.ok(!cascade.matches('.list > button', button))
    assert.ok(cascade.matches('input ~ .tag', tag))
    assert.ok(cascade.matches('input + .tag', tag))
    assert.ok(!cascade.matches('.tag ~ input', tag))
    assert.ok(cascade.matches('.row:last-child .tag', tag))
    assert.ok(!cascade.matches('.row:first-child .tag', tag))
  })

  test('read attributes, :not, :is and form state', () => {
    const { button, input } = page()
    assert.ok(cascade.matches('[type="button"]', button))
    assert.ok(cascade.matches('input[type=text]:disabled', input))
    assert.ok(cascade.matches('button:enabled', button))
    assert.ok(cascade.matches(':root:not([data-theme="light"]) .go', button))
    assert.ok(!cascade.matches('.go:not(button)', button))
    assert.ok(cascade.matches(':is(.nope, .go)', button))
  })

  test('hover and focus reach the ancestors a browser gives them to', () => {
    const { button, list } = page()
    const hovered = cascade.interaction({ hover: button })
    assert.ok(cascade.matches('.list:hover .go', button, hovered))
    assert.ok(!cascade.matches('.list:hover .go', button))
    const focused = cascade.interaction({ focus: button })
    assert.ok(cascade.matches('.go:focus-visible', button, focused))
    assert.ok(cascade.matches('.list:focus-within', list, focused))
    assert.ok(!cascade.matches('.list:focus', list, focused))
  })

  test('a selector for a pseudo-element does not select the element', () => {
    const { button } = page()
    assert.ok(!cascade.matches('.go::before', button))
    assert.ok(!cascade.matches('.go:after', button))
  })

  test('throw on what they cannot evaluate, but only for an element they could select', () => {
    const { button } = page()
    assert.throws(() => cascade.matches('.go:has(span)', button), /cannot evaluate :has/)
    assert.ok(!cascade.matches('.other:has(span)', button))
  })

  test('weigh ids, then classes, then types', () => {
    assert.deepEqual(cascade.specificity('#a .b c'), [1, 1, 1])
    assert.deepEqual(cascade.specificity(':root:not([x]) .b::before'), [0, 3, 1])
    assert.deepEqual(cascade.specificity(':where(#a) .b'), [0, 1, 0])
  })
})

describe('the cascade', () => {
  const css = `
    html { font-size: 20px; }
    .go { min-height: 1rem; display: inline-flex; }
    button.go { min-height: 2em; }
    .go { min-height: 5px; }
    ul .go { font-size: .5rem; }
    .hide { display: none !important; }
    #list .go.hide { display: block; }
    @media (max-width: 900px) {
      .go { visibility: hidden; }
    }
    .row { pointer-events: none; }
  `
  const rules = cascade.parseStylesheet(css.replace(/\n/g, '\r\n'))

  test('specificity beats order, and order breaks a tie', () => {
    const { button } = page()
    // button.go outweighs both .go rules; its 2em is 2 x .5rem of a 20px root.
    assert.equal(cascade.px(rules, button, 'min-height'), 20)
    const tie = cascade.parseStylesheet('.go { min-height: 3px; } .row .go { min-height: 9px; } .go { min-height: 4px; }')
    assert.equal(cascade.px(tie, button, 'min-height'), 9)
    button.classes.delete('go')
    button.classes.add('other')
    const later = cascade.parseStylesheet('.other { min-height: 3px; } .other { min-height: 4px; }')
    assert.equal(cascade.px(later, button, 'min-height'), 4)
  })

  test('!important beats any specificity', () => {
    const { button } = page()
    button.classes.add('hide')
    assert.equal(cascade.computed(rules, button, 'display'), 'none')
    assert.match(cascade.unreachable(rules, button), /button\.go\.hide is display: none \(\.hide\)/)
  })

  test('a rule inside @media applies only with its condition on', () => {
    const { button } = page()
    assert.deepEqual(cascade.conditions(rules), ['(max-width: 900px)'])
    assert.equal(cascade.computed(rules, button, 'visibility', { env: {} }), 'visible')
    assert.equal(cascade.computed(rules, button, 'visibility', { env: { '(max-width: 900px)': true } }), 'hidden')
    assert.equal(cascade.combinations(['a', 'b']).length, 4)
  })

  test('inherited properties come down from the ancestors', () => {
    const { button, tag } = page()
    assert.equal(cascade.computed(rules, button, 'pointer-events'), 'none')
    assert.match(cascade.unreachable(rules, tag), /pointer-events: none/)
  })

  test('a hidden ancestor hides the element', () => {
    const { list, button } = page()
    list.attributes.set('hidden', '')
    const hiding = cascade.parseStylesheet('[hidden] { display: none !important; }')
    assert.match(cascade.unreachable(hiding, button), /ul#list\.list is display: none \(\[hidden\]\)/)
  })

  test('the last declaration of a property in one rule wins', () => {
    const { button } = page()
    const twice = cascade.parseStylesheet('.go { min-height: 3px; min-height: 8px; display: none; display: block; }')
    assert.equal(cascade.px(twice, button, 'min-height'), 8)
    assert.equal(cascade.computed(twice, button, 'display'), 'block')
  })

  test('opacity 0 on the element or any ancestor hides it', () => {
    const { list, button } = page()
    const fade = cascade.parseStylesheet('.list { opacity: 0; }')
    assert.match(cascade.unreachable(fade, button), /ul#list\.list is opacity: 0 \(\.list\)/)
    const clear = cascade.parseStylesheet('.go { opacity: 0%; }')
    assert.match(cascade.unreachable(clear, button), /button\.go is opacity: 0/)
    const half = cascade.parseStylesheet('.list { opacity: .5; }')
    assert.equal(cascade.unreachable(half, button), null)
    assert.equal(cascade.unreachable(fade, list.parent), null)
  })

  test('visibility other than visible hides the element, and a child can show itself again', () => {
    const { button, tag } = page()
    const hidden = cascade.parseStylesheet('.go { visibility: hidden; }')
    assert.match(cascade.unreachable(hidden, button), /button\.go is visibility: hidden/)
    const collapsed = cascade.parseStylesheet('.row { visibility: collapse; }')
    assert.match(cascade.unreachable(collapsed, button), /visibility: collapse/)
    const above = cascade.parseStylesheet('.list { visibility: hidden; } .go { visibility: visible; }')
    assert.equal(cascade.unreachable(above, button), null)
    assert.match(cascade.unreachable(above, tag), /span\.tag is visibility: hidden/)
  })

  test('the least size is the larger of the minimum and a fixed length', () => {
    const { button } = page()
    const read = (css, axis) => cascade.leastSize(cascade.parseStylesheet(css), button, axis)
    assert.equal(read('.go { }', 'height'), 0)
    assert.equal(read('.go { min-height: 20px; }', 'height'), 20)
    assert.equal(read('.go { height: 24px; }', 'height'), 24)
    assert.equal(read('.go { height: 10px; min-height: 24px; }', 'height'), 24)
    assert.equal(read('.go { height: 30px; min-height: 24px; }', 'height'), 30)
    assert.equal(read('.go { width: 1.5rem; }', 'width'), 24)
    assert.throws(() => read('.go { height: 50%; }', 'height'), /cannot resolve height/)
  })

  test('refuses a length it cannot resolve', () => {
    const { button } = page()
    const calc = cascade.parseStylesheet('.go { min-height: calc(1rem + 2px); }')
    assert.throws(() => cascade.px(calc, button, 'min-height'), /cannot resolve min-height/)
    const custom = cascade.parseStylesheet('.go { display: var(--shown); }')
    assert.throws(() => cascade.unreachable(custom, button), /cannot resolve display/)
  })

  test('refuses a stylesheet it would only half read', () => {
    assert.throws(() => cascade.parseStylesheet('.a { color: red; } } .b { display: none; }'), /unmatched \}/)
    assert.throws(() => cascade.parseStylesheet('@supports (display: grid) { .a { display: none; } }'), /cannot read the at-rule/)
  })
})
