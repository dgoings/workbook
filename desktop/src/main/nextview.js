'use strict'

// The Next view's data: what `workbook next` would pick in every imported
// project, read through the CLI rather than recomputed here, so the tool's own
// rule — next-tagged status, dependencies done, skip what somebody else holds —
// is the only rule there is. The runner is a parameter so the shaping can be
// tested without a binary.

const workbook = require('./workbook')

const MIN_LIMIT = 1
const MAX_LIMIT = 20

/** Whole number between 1 and 20; anything unparseable is 1. */
function clampLimit (value) {
  const parsed = Math.floor(Number(value))
  if (!Number.isFinite(parsed)) return MIN_LIMIT
  return Math.min(MAX_LIMIT, Math.max(MIN_LIMIT, parsed))
}

/** The members the view draws, and nothing a task carries that it does not. */
function shapeTask (task) {
  return {
    id: String(task.id ?? ''),
    title: String(task.title ?? ''),
    priority: String(task.priority ?? ''),
    status: String(task.status ?? ''),
    labels: Array.isArray(task.labels) ? task.labels.map(String) : [],
    assignees: Array.isArray(task.assignments)
      ? task.assignments.map((assignment) => String(assignment?.principal ?? '')).filter(Boolean)
      : [],
    updatedAt: String(task.updatedAt ?? '')
  }
}

/**
 * Read every project's next tasks at once.
 *
 * One project failing — a repository moved, a binary too old for --limit —
 * answers with its error beside an empty list, so the others still show.
 *
 * @param {{ projects: Array<{id:string,key:string,name:string,path:string,status?:string}>, limit: unknown, run?: (repoPath: string, limit: number) => Promise<{tasks: object[], eligible: number}> }} input
 */
async function loadNext ({ projects, limit, run = workbook.nextTasks }) {
  const wanted = clampLimit(limit)
  const outcomes = await Promise.allSettled(projects.map((project) => run(project.path, wanted)))
  return {
    limit: wanted,
    projects: projects.map((project, index) => {
      const outcome = outcomes[index]
      const base = {
        id: project.id,
        key: project.key,
        name: project.name,
        // The board server's health, carried through rather than looked up
        // again: a project whose server is not running answers from a repository
        // nothing is keeping synchronized, and the view has to be able to say
        // so. A project the supervisor has never started is simply stopped.
        status: String(project.status ?? 'stopped')
      }
      if (outcome.status === 'rejected') {
        const error = outcome.reason?.message ?? String(outcome.reason)
        return { ...base, tasks: [], eligible: 0, error }
      }
      const data = outcome.value ?? {}
      const tasks = Array.isArray(data.tasks) ? data.tasks.map(shapeTask) : []
      const eligible = Number.isFinite(Number(data.eligible)) ? Number(data.eligible) : tasks.length
      return { ...base, tasks, eligible, error: null }
    })
  }
}

module.exports = { loadNext, clampLimit, MIN_LIMIT, MAX_LIMIT }
