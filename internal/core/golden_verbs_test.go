package core

import "testing"

// Golden task-ref fixtures for every task verb this build ships.
//
// The table beside this one, in golden_test.go, pins the status alphabet a
// stored document may hold. This one pins the other axis: one fixture per
// mutation a person can run, captured as real Git blobs out of a repository
// driven by the command line — `git show <commit>:operation.json` and `git
// show <commit>:state.json` over the whole chain of a task that was created,
// moved, retitled, re-statused, made to depend and stop depending, stripped of
// labels, deleted, restored, deleted again, and restored into a status.
//
// It exists because the comments and attachments work is the first change to
// raise SupportedFormatGeneration, and the marker's whole value rests on a
// claim that is easy to state and easy to break by accident: a pack that uses
// none of the new semantics carries no marker and encodes to exactly the bytes
// it encoded to before. A table covering only the create and status verbs
// would let a stray struct field, a lost `omitempty`, or a fold that started
// materializing an empty collection slip through on every other verb.
//
// The assertions are the ones golden_test.go documents: the stored bytes
// decode, the decoded value re-encodes to those same bytes, and the checkpoint
// still validates against its parent. Do not regenerate this table to make a
// failing test pass. A failure here is the finding.
var goldenTaskVerbRefs = []goldenTaskRef{
	{
		name: "create",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-13T20:18:50.132699-04:00","operations":[{"id":"01KZYT5GMMXWMFKBK55XP1E69C","type":"task.create","task":{"title":"Alpha task","description":"","status":"backlog","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:50.132699-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":1,"task":{"title":"Alpha task","description":"","status":"backlog","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:50.132699-04:00","deleted":false}}
`,
	},
	{
		name: "move --after",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":1,"task":{"title":"Alpha task","description":"","status":"backlog","priority":"medium","labels":[],"rank":"1/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:50.132699-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":2,"wallTime":"2026-08-13T20:18:50.699865-04:00","operations":[{"id":"01KZYT5H6B46CG7AAD721S1PQR","type":"field.set","field":"rank","value":"3/1"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":2,"task":{"title":"Alpha task","description":"","status":"backlog","priority":"medium","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:50.699865-04:00","deleted":false}}
`,
	},
	{
		name: "update --title --description --priority --label --label",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":2,"task":{"title":"Alpha task","description":"","status":"backlog","priority":"medium","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:50.699865-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":3,"wallTime":"2026-08-13T20:18:50.966405-04:00","operations":[{"id":"01KZYT5HEPAE25MDKHZ2A84XAB","type":"field.set","field":"description","value":"Prose body"},{"id":"01KZYT5HEPNHX641XV1HVMK3VG","type":"field.set","field":"priority","value":"high"},{"id":"01KZYT5HEPCV355A84EEFAWKGP","type":"field.set","field":"title","value":"Alpha renamed"},{"id":"01KZYT5HEPGTZVSJ6A0G3XBSJA","type":"set.add","field":"labels","value":"storage"},{"id":"01KZYT5HEP170D0A3JKD7229T5","type":"set.add","field":"labels","value":"ui"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":3,"task":{"title":"Alpha renamed","description":"Prose body","status":"backlog","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:50.966405-04:00","deleted":false}}
`,
	},
	{
		name: "update --status",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":3,"task":{"title":"Alpha renamed","description":"Prose body","status":"backlog","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:50.966405-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":4,"wallTime":"2026-08-13T20:18:51.243902-04:00","operations":[{"id":"01KZYT5HQBN2AVD4D96DQAMYM7","type":"field.set","field":"status","value":"in-progress"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":4,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:51.243902-04:00","deleted":false}}
`,
	},
	{
		name: "depend",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":4,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:51.243902-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":5,"wallTime":"2026-08-13T20:18:51.555812-04:00","operations":[{"id":"01KZYT5J13TCVCVTJXAXE2V4Y1","type":"set.add","field":"dependencies","value":"GD-01KZYT5GX1K3W87D30FBCD2H41"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":5,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":["GD-01KZYT5GX1K3W87D30FBCD2H41"],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:51.555812-04:00","deleted":false}}
`,
	},
	{
		name: "free",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":5,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":["GD-01KZYT5GX1K3W87D30FBCD2H41"],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:51.555812-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":6,"wallTime":"2026-08-13T20:18:51.827416-04:00","operations":[{"id":"01KZYT5J9KD3A6R2BHJN3B1CDY","type":"set.remove","field":"dependencies","value":"GD-01KZYT5GX1K3W87D30FBCD2H41"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":6,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:51.827416-04:00","deleted":false}}
`,
	},
	{
		name: "update --label (drops one)",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":6,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["storage","ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:51.827416-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":7,"wallTime":"2026-08-13T20:18:52.098686-04:00","operations":[{"id":"01KZYT5JJ256RBKCXACHWEK4K8","type":"set.remove","field":"labels","value":"storage"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":7,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.098686-04:00","deleted":false}}
`,
	},
	{
		name: "update --clear-labels",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":7,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":["ui"],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.098686-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":8,"wallTime":"2026-08-13T20:18:52.37635-04:00","operations":[{"id":"01KZYT5JTRA2RTP750JGHN8KP6","type":"set.remove","field":"labels","value":"ui"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":8,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.37635-04:00","deleted":false}}
`,
	},
	{
		name: "delete",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":8,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.37635-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":9,"wallTime":"2026-08-13T20:18:52.652364-04:00","operations":[{"id":"01KZYT5K3CDM95KA8CS7CZ7HGR","type":"task.tombstone"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":9,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.652364-04:00","deleted":true}}
`,
	},
	{
		name: "restore",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":9,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.652364-04:00","deleted":true}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":10,"wallTime":"2026-08-13T20:18:52.915797-04:00","operations":[{"id":"01KZYT5KBKF667193PQ2H6K6B9","type":"task.restore"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":10,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.915797-04:00","deleted":false}}
`,
	},
	{
		name: "delete (second time)",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":10,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:52.915797-04:00","deleted":false}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":11,"wallTime":"2026-08-13T20:18:53.180532-04:00","operations":[{"id":"01KZYT5KKWN9TEKMP8FPERSF88","type":"task.tombstone"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":11,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:53.180532-04:00","deleted":true}}
`,
	},
	{
		name: "restore --into",
		parent: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":11,"task":{"title":"Alpha renamed","description":"Prose body","status":"in-progress","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:53.180532-04:00","deleted":true}}
`,
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","historyGeneration":"01KZYT5GMM4CXNJ4PY0N5AKPWV","actor":{"id":"t@example.test"},"logicalClock":12,"wallTime":"2026-08-13T20:18:53.432038-04:00","operations":[{"id":"01KZYT5KVRCCXE72JQ5HE0AV4A","type":"task.restore"},{"id":"01KZYT5KVR45A25KTNX87284QT","type":"field.set","field":"status","value":"done"}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GMM8B6BJF3WYAPMGZQR","history":{"generation":"01KZYT5GMM4CXNJ4PY0N5AKPWV","compactedFrom":null},"logicalClock":12,"task":{"title":"Alpha renamed","description":"Prose body","status":"done","priority":"high","labels":[],"rank":"3/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.132699-04:00","updatedAt":"2026-08-13T20:18:53.432038-04:00","deleted":false}}
`,
	},
	{
		name: "create (second task)",
		operation: `{"format":"workbook.operation-pack","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GX1K3W87D30FBCD2H41","historyGeneration":"01KZYT5GX1M449SYDJPN4W49DZ","actor":{"id":"t@example.test"},"logicalClock":1,"wallTime":"2026-08-13T20:18:50.401967-04:00","operations":[{"id":"01KZYT5GX1JXW05ER8GVC74KC9","type":"task.create","task":{"title":"Beta task","description":"","status":"backlog","priority":"medium","labels":[],"rank":"2/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.401967-04:00","updatedAt":"2026-08-13T20:18:50.401967-04:00","deleted":false}}]}
`,
		state: `{"format":"workbook.task-state","version":1,"projectId":"01KZYT5G3FCQ1Q9AXF888S30C8","taskId":"GD-01KZYT5GX1K3W87D30FBCD2H41","history":{"generation":"01KZYT5GX1M449SYDJPN4W49DZ","compactedFrom":null},"logicalClock":1,"task":{"title":"Beta task","description":"","status":"backlog","priority":"medium","labels":[],"rank":"2/1","dependencies":[],"createdAt":"2026-08-13T20:18:50.401967-04:00","updatedAt":"2026-08-13T20:18:50.401967-04:00","deleted":false}}
`,
	},
}

func TestGoldenTaskVerbRefsDecodeToTheSameBytes(t *testing.T) {
	for _, fixture := range goldenTaskVerbRefs {
		t.Run(fixture.name, func(t *testing.T) {
			assertGoldenBytes(t, fixture)
		})
	}
}

func TestGoldenTaskVerbRefsStillValidateAsCheckpoints(t *testing.T) {
	for _, fixture := range goldenTaskVerbRefs {
		t.Run(fixture.name, func(t *testing.T) {
			assertGoldenCheckpoint(t, fixture)
		})
	}
}
