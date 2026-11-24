package projection

// schemaVersion is bumped to 9 because the cache now holds relics: one row per
// relic, its properties, its parts with their fields and the object ID, format
// and size of each body, and its recorded operations. Body text is never
// copied in; it stays in Git and is read by object ID. A cache stamped earlier
// has no relic tables, so it is discarded and Git read again, as every earlier
// bump did.
//
// Version 8 was bumped because a task row now keeps the task's due
// date. A cache stamped earlier has no column for it, and a mutation folded
// onto a snapshot that dropped it would write a state.json without the date —
// silently clearing it for every clone — so the cache is discarded and Git read
// again, which is the same answer every earlier bump gave. A task with no due
// date stores the empty string.
//
// Version 7 was bumped because a task row now keeps the history's
// compactedFrom, the tip a checkpoint replaced. A mutation folds its parent's
// history metadata into the state it writes, so an edit built on a cached
// snapshot that dropped it would write a state.json that replaying the history
// from Git does not compute, and validation would call the task corrupt. A
// cache stamped earlier has no column for it, so it is discarded and Git read
// again.
//
// Version 6 was the drop compaction records on an attachment it did not keep:
// a thread a cache stored without that field would serve a dropped file as one
// whose bytes can still be downloaded.
//
// Version 5 was a task's comments and attachments, and the payload of the
// operations that record them.
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
const schemaVersion = "9"

const schema = `
CREATE TABLE projection_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE tasks (
  task_id TEXT PRIMARY KEY, head TEXT NOT NULL, project_id TEXT NOT NULL,
  history_generation TEXT NOT NULL, compacted_from TEXT NOT NULL,
  logical_clock INTEGER NOT NULL, min_reader INTEGER NOT NULL,
  title TEXT NOT NULL, description TEXT NOT NULL, status TEXT NOT NULL,
  priority TEXT NOT NULL, rank TEXT NOT NULL, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, deleted INTEGER NOT NULL,
  thread TEXT NOT NULL, due TEXT NOT NULL
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
CREATE INDEX operations_by_commit ON operations (task_id, commit_id);
CREATE TABLE relics (
  relic_id TEXT PRIMARY KEY, head TEXT NOT NULL, project_id TEXT NOT NULL,
  state_version INTEGER NOT NULL, min_reader INTEGER NOT NULL,
  history_generation TEXT NOT NULL, compacted_from TEXT,
  logical_clock INTEGER NOT NULL, title TEXT NOT NULL, type TEXT NOT NULL,
  schema TEXT NOT NULL, author TEXT NOT NULL, retired INTEGER NOT NULL,
  deleted INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  part_count INTEGER NOT NULL
);
CREATE INDEX relics_by_type ON relics (type, relic_id);
CREATE TABLE relic_properties (
  relic_id TEXT NOT NULL, name TEXT NOT NULL, value TEXT NOT NULL,
  PRIMARY KEY (relic_id, name)
);
CREATE TABLE relic_parts (
  relic_id TEXT NOT NULL, part_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
  type TEXT NOT NULL, position TEXT NOT NULL, removed INTEGER NOT NULL,
  fields_object TEXT NOT NULL, fields TEXT NOT NULL,
  body_object TEXT, body_format TEXT, body_version TEXT, body_bytes INTEGER,
  PRIMARY KEY (relic_id, part_id)
);
CREATE INDEX relic_parts_by_order ON relic_parts (relic_id, ordinal);
CREATE TABLE relic_operations (
  relic_id TEXT NOT NULL, commit_id TEXT NOT NULL, chain_index INTEGER NOT NULL,
  pack_index INTEGER NOT NULL, operation_id TEXT NOT NULL,
  logical_clock INTEGER NOT NULL, history_generation TEXT NOT NULL,
  min_reader INTEGER NOT NULL, actor TEXT NOT NULL, wall_time TEXT NOT NULL,
  type TEXT NOT NULL, part TEXT NOT NULL, payload TEXT NOT NULL,
  PRIMARY KEY (relic_id, chain_index, pack_index)
);
CREATE INDEX relic_operations_by_id ON relic_operations (operation_id);`
