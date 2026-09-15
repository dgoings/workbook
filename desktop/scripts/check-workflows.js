#!/usr/bin/env node
'use strict'

// Check that every workflow file still has the shape GitHub needs.
//
// A workflow file that does not parse is not a failing workflow — it is an
// absent one. GitHub reports it by its filename instead of its `name`, runs
// nothing useful, and a release tag can build nothing at all while looking like
// an ordinary failure. That happened here once, from an edit that removed a
// `steps:` key, and nothing in CI could have noticed.
//
// This is a shape check, not a YAML parser: Node has no YAML built in, and the
// mistakes worth catching are structural — a job with no steps, a file with no
// name — rather than subtle.

const fs = require('node:fs')
const path = require('node:path')

const directory = path.join(__dirname, '..', '.github', 'workflows')

/** Keys nested directly under a top-level key, by indentation. */
function childKeys (lines, parent) {
  const start = lines.findIndex((line) => line === `${parent}:`)
  if (start === -1) return null

  const keys = []
  for (let i = start + 1; i < lines.length; i += 1) {
    const line = lines[i]
    if (!line.trim() || line.trimStart().startsWith('#')) continue
    if (!line.startsWith(' ')) break // back to the top level
    const match = /^ {2}([A-Za-z_][\w-]*):/.exec(line)
    if (match) keys.push({ name: match[1], line: i })
  }
  return keys
}

let failures = 0
for (const file of fs.readdirSync(directory).filter((f) => /\.ya?ml$/.test(f))) {
  const lines = fs.readFileSync(path.join(directory, file), 'utf8').split('\n')
  const problems = []

  for (const key of ['name', 'on', 'jobs']) {
    if (!lines.some((line) => line.startsWith(`${key}:`))) {
      problems.push(`no top-level ${key}:`)
    }
  }

  const jobs = childKeys(lines, 'jobs') ?? []
  if (jobs.length === 0) problems.push('no jobs')

  for (const [index, job] of jobs.entries()) {
    // A job's own keys sit four spaces in; the next job bounds the search.
    const end = jobs[index + 1]?.line ?? lines.length
    const body = lines.slice(job.line, end)
    const hasSteps = body.some((line) => /^ {4}steps:\s*$/.test(line))
    const usesWorkflow = body.some((line) => /^ {4}uses:/.test(line))
    if (!hasSteps && !usesWorkflow) problems.push(`job "${job.name}" has no steps:`)
  }

  if (problems.length > 0) {
    failures += 1
    console.error(`${file}:`)
    for (const problem of problems) console.error(`  - ${problem}`)
  } else {
    console.log(`${file}: ${jobs.length} job(s) — ${jobs.map((j) => j.name).join(', ')}`)
  }
}

if (failures > 0) {
  console.error(`\n${failures} workflow file(s) malformed`)
  process.exit(1)
}
