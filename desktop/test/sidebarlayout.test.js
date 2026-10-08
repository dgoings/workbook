'use strict'

// The sidebar's order and categories as a pure value: what an older registry
// reads as, how a stale or hand-edited layout is repaired, the order shortcuts
// count in, and every move the sidebar can make. Nothing here loads Electron or
// touches a disk.

const test = require('node:test')
const assert = require('node:assert/strict')

const {
  normalize,
  flatten,
  moveProject,
  moveCategory,
  createCategory,
  renameCategory,
  setCategoryCollapsed,
  deleteCategory
} = require('../src/main/sidebarlayout')

const top = (id) => ({ kind: 'project', id })
const category = (id, name, projects, collapsed = false) =>
  ({ kind: 'category', id, name, collapsed, projects })

/**
 * a, [Work: b, c], d, [Home (collapsed): e]. Built fresh for each use so a
 * mutation in one case cannot leak into the next.
 */
function sample () {
  return {
    items: [
      top('a'),
      category('work', 'Work', ['b', 'c']),
      top('d'),
      category('home', 'Home', ['e'], true)
    ]
  }
}

/** Run `operation` on a deep-frozen sample, so any mutation throws. */
function frozen () {
  const layout = sample()
  for (const item of layout.items) {
    if (item.projects) Object.freeze(item.projects)
    Object.freeze(item)
  }
  Object.freeze(layout.items)
  return Object.freeze(layout)
}

// normalize

test('normalize reads an older registry with no layout as every project at the top level in stored order', () => {
  assert.deepEqual(normalize(undefined, ['x', 'y', 'z']), { items: [top('x'), top('y'), top('z')] })
})

test('normalize reads a layout of the wrong shape as the default', () => {
  const ids = ['x', 'y']
  const expected = { items: [top('x'), top('y')] }
  for (const wrong of [null, 'layout', 42, [], { items: 'no' }, { items: {} }, {}]) {
    assert.deepEqual(normalize(wrong, ids), expected, `for ${JSON.stringify(wrong)}`)
  }
})

test('normalize leaves a well-formed layout as it is', () => {
  assert.deepEqual(normalize(sample(), ['a', 'b', 'c', 'd', 'e']), sample())
})

test('normalize drops project ids the registry no longer holds, from the top level and from categories', () => {
  assert.deepEqual(normalize(sample(), ['a', 'c', 'e']), {
    items: [top('a'), category('work', 'Work', ['c']), category('home', 'Home', ['e'], true)]
  })
})

test('normalize keeps an emptied category', () => {
  const layout = normalize(sample(), ['a', 'd', 'e'])
  assert.deepEqual(layout.items[1], category('work', 'Work', []))
})

test('normalize appends projects the layout does not mention at the top level, in registry order', () => {
  assert.deepEqual(normalize(sample(), ['z', 'a', 'b', 'c', 'd', 'e', 'y']).items.slice(-2), [top('z'), top('y')])
})

test('normalize keeps only the first place of a duplicated project id', () => {
  const layout = {
    items: [top('a'), category('work', 'Work', ['a', 'b', 'b']), top('b')]
  }
  assert.deepEqual(normalize(layout, ['a', 'b']), {
    items: [top('a'), category('work', 'Work', ['b'])]
  })
})

test('normalize dedupes the registry ids it is given', () => {
  assert.deepEqual(normalize(undefined, ['a', 'a', 'b']), { items: [top('a'), top('b')] })
})

test('normalize returns a malformed category\'s projects to the top level where it stood', () => {
  const layout = {
    items: [
      top('a'),
      category('', 'No id', ['b']),
      { kind: 'category', id: 'nameless', projects: ['c'] },
      category('blank', '   ', ['d']),
      top('e')
    ]
  }
  assert.deepEqual(normalize(layout, ['a', 'b', 'c', 'd', 'e']), {
    items: [top('a'), top('b'), top('c'), top('d'), top('e')]
  })
})

test('normalize drops a second category with the same id, keeping its projects in place', () => {
  const layout = { items: [category('k', 'One', ['a']), category('k', 'Two', ['b'])] }
  assert.deepEqual(normalize(layout, ['a', 'b']), {
    items: [category('k', 'One', ['a']), top('b')]
  })
})

test('normalize coerces a category\'s fields strictly', () => {
  const layout = {
    items: [
      { kind: 'category', id: 'k', name: '  Work  ', collapsed: 'true', projects: 'a' },
      { kind: 'category', id: 'm', name: 'Home', collapsed: true, projects: ['a', 7, null] }
    ]
  }
  assert.deepEqual(normalize(layout, ['a']), {
    items: [category('k', 'Work', []), category('m', 'Home', ['a'], true)]
  })
})

test('normalize drops items of an unknown kind or with a non-string id, and strips extra fields', () => {
  const layout = {
    items: [null, 'a', { kind: 'folder', id: 'a' }, { kind: 'project', id: 1 }, { kind: 'project', id: 'b', extra: 1 }]
  }
  assert.deepEqual(normalize(layout, ['a', 'b']), { items: [top('b'), top('a')] })
})

test('normalize does not mutate the stored layout', () => {
  const layout = frozen()
  assert.doesNotThrow(() => normalize(layout, ['a']))
  assert.deepEqual(layout, sample())
})

test('normalize returns objects that share nothing with its input', () => {
  const layout = sample()
  const result = normalize(layout, ['a', 'b', 'c', 'd', 'e'])
  result.items[1].projects.push('zz')
  result.items[0].id = 'zz'
  assert.deepEqual(layout, sample())
})

// flatten

test('flatten lists projects top to bottom, counting collapsed categories', () => {
  assert.deepEqual(flatten(sample()), ['a', 'b', 'c', 'd', 'e'])
})

test('flatten of an empty layout is empty', () => {
  assert.deepEqual(flatten({ items: [] }), [])
  assert.deepEqual(flatten({ items: [category('k', 'Empty', [])] }), [])
})

test('folding a category does not change the flattened order', () => {
  const folded = setCategoryCollapsed(sample(), 'work', true)
  assert.deepEqual(flatten(folded), flatten(sample()))
})

// moveProject

test('moveProject reorders within the top level', () => {
  const moved = moveProject(sample(), 'd', { kind: 'top', index: 0 })
  assert.deepEqual(flatten(moved), ['d', 'a', 'b', 'c', 'e'])
  assert.deepEqual(moved.items[0], top('d'))
})

test('moveProject counts the index before the move, so moving down lands in the named gap', () => {
  // Gap 3 is between d and Home: a moves from before Work to just after d.
  const moved = moveProject(sample(), 'a', { kind: 'top', index: 3 })
  assert.deepEqual(moved.items.map((item) => item.id), ['work', 'd', 'a', 'home'])
})

test('moveProject to either gap beside its own row leaves the layout as it was', () => {
  // d is top-level item 2: gaps 2 and 3 are directly above and below it.
  assert.deepEqual(moveProject(sample(), 'd', { kind: 'top', index: 2 }), sample())
  assert.deepEqual(moveProject(sample(), 'd', { kind: 'top', index: 3 }), sample())
  // And inside a category: c is Work's entry 1.
  assert.deepEqual(moveProject(sample(), 'c', { kind: 'category', id: 'work', index: 1 }), sample())
  assert.deepEqual(moveProject(sample(), 'c', { kind: 'category', id: 'work', index: 2 }), sample())
})

test('moveProject takes a project into a category', () => {
  const moved = moveProject(sample(), 'a', { kind: 'category', id: 'work', index: 1 })
  assert.deepEqual(moved, {
    items: [category('work', 'Work', ['b', 'a', 'c']), top('d'), category('home', 'Home', ['e'], true)]
  })
})

test('moveProject takes a project out of a category to the top level', () => {
  const moved = moveProject(sample(), 'b', { kind: 'top', index: 0 })
  assert.deepEqual(moved.items.slice(0, 3), [top('b'), top('a'), category('work', 'Work', ['c'])])
})

test('moveProject moves a project between categories', () => {
  const moved = moveProject(sample(), 'c', { kind: 'category', id: 'home', index: 0 })
  assert.deepEqual(moved.items[1], category('work', 'Work', ['b']))
  assert.deepEqual(moved.items[3], category('home', 'Home', ['c', 'e'], true))
})

test('moveProject reorders within a category', () => {
  const moved = moveProject(sample(), 'c', { kind: 'category', id: 'work', index: 0 })
  assert.deepEqual(moved.items[1].projects, ['c', 'b'])
})

test('moveProject into a collapsed category lands there and leaves it collapsed', () => {
  const moved = moveProject(sample(), 'a', { kind: 'category', id: 'home', index: 1 })
  assert.deepEqual(moved.items.at(-1), category('home', 'Home', ['e', 'a'], true))
  assert.deepEqual(flatten(moved), ['b', 'c', 'd', 'e', 'a'])
})

test('moveProject clamps an index past either end', () => {
  assert.deepEqual(moveProject(sample(), 'a', { kind: 'top', index: 99 }).items.at(-1), top('a'))
  assert.deepEqual(moveProject(sample(), 'd', { kind: 'top', index: -5 }).items[0], top('d'))
  assert.deepEqual(moveProject(sample(), 'd', { kind: 'category', id: 'work', index: 99 }).items[1].projects, ['b', 'c', 'd'])
})

test('moveProject rejects an unknown project, an unknown category, a bad kind or a non-integer index', () => {
  assert.throws(() => moveProject(sample(), 'zz', { kind: 'top', index: 0 }), /no project/)
  assert.throws(() => moveProject(sample(), 'a', { kind: 'category', id: 'zz', index: 0 }), /no category/)
  assert.throws(() => moveProject(sample(), 'a', { kind: 'side', index: 0 }), /target.kind/)
  assert.throws(() => moveProject(sample(), 'a', { kind: 'top', index: 1.5 }), /integer/)
  assert.throws(() => moveProject(sample(), 'a', { kind: 'top', index: '1' }), /integer/)
  assert.throws(() => moveProject(sample(), 'a', null), /target/)
  // A category id is not a project id.
  assert.throws(() => moveProject(sample(), 'work', { kind: 'top', index: 0 }), /no project/)
})

test('moveProject does not mutate its input', () => {
  const layout = frozen()
  moveProject(layout, 'a', { kind: 'category', id: 'work', index: 0 })
  moveProject(layout, 'b', { kind: 'top', index: 0 })
  moveProject(layout, 'c', { kind: 'category', id: 'work', index: 0 })
  assert.deepEqual(layout, sample())
})

// moveCategory

test('moveCategory moves a category up the top level', () => {
  assert.deepEqual(moveCategory(sample(), 'home', 0).items.map((item) => item.id), ['home', 'a', 'work', 'd'])
})

test('moveCategory counts the index before the move, and keeps the category\'s projects with it', () => {
  const moved = moveCategory(sample(), 'work', 4)
  assert.deepEqual(moved.items.map((item) => item.id), ['a', 'd', 'home', 'work'])
  assert.deepEqual(moved.items.at(-1), category('work', 'Work', ['b', 'c']))
  assert.deepEqual(flatten(moved), ['a', 'd', 'e', 'b', 'c'])
})

test('moveCategory to either gap beside itself leaves the layout as it was', () => {
  assert.deepEqual(moveCategory(sample(), 'work', 1), sample())
  assert.deepEqual(moveCategory(sample(), 'work', 2), sample())
})

test('moveCategory clamps, and rejects an unknown category or a non-integer index', () => {
  assert.deepEqual(moveCategory(sample(), 'work', -3).items[0].id, 'work')
  assert.throws(() => moveCategory(sample(), 'zz', 0), /no category/)
  assert.throws(() => moveCategory(sample(), 'a', 0), /no category/)
  assert.throws(() => moveCategory(sample(), 'work', NaN), /integer/)
})

test('moveCategory does not mutate its input', () => {
  const layout = frozen()
  moveCategory(layout, 'home', 0)
  assert.deepEqual(layout, sample())
})

// createCategory

test('createCategory appends an empty, expanded category with the caller\'s id and a trimmed name', () => {
  const created = createCategory(sample(), 'new-id', '  Later  ')
  assert.deepEqual(created.items.at(-1), category('new-id', 'Later', []))
  assert.equal(created.items.length, 5)
  assert.deepEqual(flatten(created), flatten(sample()))
})

test('createCategory rejects a missing or duplicate id and an empty name', () => {
  assert.throws(() => createCategory(sample(), '', 'Name'), /id/)
  assert.throws(() => createCategory(sample(), undefined, 'Name'), /id/)
  assert.throws(() => createCategory(sample(), 'work', 'Name'), /already exists/)
  assert.throws(() => createCategory(sample(), 'x', '   '), /name/)
  assert.throws(() => createCategory(sample(), 'x', 3), /name/)
})

test('createCategory does not mutate its input', () => {
  const layout = frozen()
  createCategory(layout, 'x', 'X')
  assert.deepEqual(layout, sample())
})

// renameCategory

test('renameCategory renames, trimmed, and leaves everything else alone', () => {
  const renamed = renameCategory(sample(), 'home', ' House ')
  assert.deepEqual(renamed.items[3], category('home', 'House', ['e'], true))
  assert.deepEqual(renamed.items.slice(0, 3), sample().items.slice(0, 3))
})

test('renameCategory rejects an empty name and an unknown category', () => {
  assert.throws(() => renameCategory(sample(), 'home', ''), /name/)
  assert.throws(() => renameCategory(sample(), 'home', null), /name/)
  assert.throws(() => renameCategory(sample(), 'zz', 'Name'), /no category/)
})

test('renameCategory does not mutate its input', () => {
  const layout = frozen()
  renameCategory(layout, 'work', 'Job')
  assert.deepEqual(layout, sample())
})

// setCategoryCollapsed

test('setCategoryCollapsed folds and unfolds', () => {
  assert.equal(setCategoryCollapsed(sample(), 'work', true).items[1].collapsed, true)
  assert.equal(setCategoryCollapsed(sample(), 'home', false).items[3].collapsed, false)
})

test('setCategoryCollapsed rejects a non-boolean and an unknown category', () => {
  assert.throws(() => setCategoryCollapsed(sample(), 'work', 'true'), /boolean/)
  assert.throws(() => setCategoryCollapsed(sample(), 'zz', true), /no category/)
})

test('setCategoryCollapsed does not mutate its input', () => {
  const layout = frozen()
  setCategoryCollapsed(layout, 'work', true)
  assert.deepEqual(layout, sample())
})

// deleteCategory

test('deleteCategory returns its projects to the top level where it stood', () => {
  const deleted = deleteCategory(sample(), 'work')
  assert.deepEqual(deleted, {
    items: [top('a'), top('b'), top('c'), top('d'), category('home', 'Home', ['e'], true)]
  })
  assert.deepEqual(flatten(deleted), flatten(sample()))
})

test('deleteCategory of a collapsed category returns its projects too', () => {
  assert.deepEqual(deleteCategory(sample(), 'home').items.at(-1), top('e'))
})

test('deleteCategory of an empty category just removes it', () => {
  const layout = createCategory(sample(), 'x', 'X')
  assert.deepEqual(deleteCategory(layout, 'x'), sample())
})

test('deleteCategory rejects an unknown category', () => {
  assert.throws(() => deleteCategory(sample(), 'zz'), /no category/)
  assert.throws(() => deleteCategory(sample(), 'a'), /no category/)
})

test('deleteCategory does not mutate its input', () => {
  const layout = frozen()
  deleteCategory(layout, 'work')
  assert.deepEqual(layout, sample())
})

test('a mutation\'s result shares no category array with its input', () => {
  const layout = sample()
  const renamed = renameCategory(layout, 'home', 'House')
  renamed.items[1].projects.push('zz')
  assert.deepEqual(layout, sample())
})

test('the module requires nothing, so it loads anywhere', () => {
  const source = require('node:fs').readFileSync(require.resolve('../src/main/sidebarlayout'), 'utf8')
  assert.doesNotMatch(source, /\brequire\s*\(/)
})
