package projection

// schemaVersion is bumped to 5 because the projection now carries a task's
// comments and attachments, and the payload of the operations that record
// them.
//
// A cache written by an earlier build has no column for either, and both are
// load-bearing rather than decorative. A mutation is folded onto the parent
// snapshot the projection served, so a thread this cache dropped would be a
// thread the next ordinary write silently erases from the checkpoint. And a
// chain replayed for `show --history` or for a comparison would replay comment
// operations with no bodies. The projection is disposable, so the answer is to
// discard it and read Git again rather than to serve half of a task.
//
// Version 4 was every task's assignments, which carries the same warning: a
// cache with no table for them reads as a task assigned to nobody, and a
// checkpoint folded onto that snapshot would publish a task with everybody's
// assignments dropped. Version 3 was the writer-format generation of every
// checkpoint and every recorded pack, where a missing column reads as
// generation zero — a claim rather than an absence, and the one claim that must
// never be made wrongly.
const schemaVersion = "5"

const schema = `
CREATE TABLE projection_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE tasks (
  task_id TEXT PRIMARY KEY, head TEXT NOT NULL, project_id TEXT NOT NULL,
  history_generation TEXT NOT NULL, logical_clock INTEGER NOT NULL,
  min_reader INTEGER NOT NULL,
  title TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL,
  priority TEXT NOT NULL, rank TEXT NOT NULL, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, deleted INTEGER NOT NULL,
  thread TEXT NOT NULL
);
CREATE TABLE task_labels (
  task_id TEXT NOT NULL, label TEXT NOT NULL, PRIMARY KEY (task_id, label)
);
CREATE TABLE task_dependencies (
  task_id TEXT NOT NULL, dependency_id TEXT NOT NULL,
  PRIMARY KEY (task_id, dependency_id)
);
CREATE TABLE task_assignments (
  task_id TEXT NOT NULL, principal TEXT NOT NULL, label TEXT NOT NULL,
  creator TEXT NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY (task_id, principal, label)
);
CREATE TABLE operations (
  operation_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, commit_id TEXT NOT NULL,
  chain_index INTEGER NOT NULL, pack_index INTEGER NOT NULL,
  logical_clock INTEGER NOT NULL, history_generation TEXT NOT NULL,
  min_reader INTEGER NOT NULL,
  actor TEXT NOT NULL, wall_time TEXT NOT NULL, type TEXT NOT NULL,
  field TEXT NOT NULL, value TEXT NOT NULL, task_data TEXT NOT NULL,
  payload TEXT NOT NULL
);
CREATE INDEX operations_by_chain ON operations (task_id, chain_index, pack_index);
CREATE INDEX operations_by_commit ON operations (task_id, commit_id);`
