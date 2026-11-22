package core

import "time"

// SetAsideEdit is one edit this clone made that a reconciliation parked and
// did not replay: the fetch found a different change to the same task on
// origin, stopped replaying this clone's operations at the conflict (see
// ConflictDetail), and kept them only in the parked tip under
// refs/workbook/reconciled/<task>/<n>.
//
// It is read from that parked tip and nothing else, so it says what the fetch
// set aside for as long as the tip is kept, across restarts, and it is never a
// second record of the conflict. It names the operation pack as it was
// recorded: who made the edit, when by the wall clock of the machine that made
// it, and the operations it carried.
type SetAsideEdit struct {
	// ParkedRef and Parked name the parked tip that holds the edit, the ref
	// and the commit it pointed at when it was read. Dismissing a set-aside
	// edit names both, so a tip that moved since it was read is not removed.
	ParkedRef string `json:"parkedRef"`
	Parked    string `json:"parked"`
	// Commit is the parked commit that recorded this edit.
	Commit     string      `json:"commit"`
	Actor      string      `json:"actor"`
	WallTime   time.Time   `json:"wallTime"`
	Operations []Operation `json:"operations"`
}
