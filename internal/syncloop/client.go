package syncloop

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// ErrNoWatcher reports that nothing is listening for this repository. It is the
// ordinary case, not a failure: a command that sees it takes the inline path.
var ErrNoWatcher = errors.New("no Workbook watcher is listening for this repository")

// Client is one short-lived connection to a watcher. Every exchange is a single
// request and response on a fresh connection, so a wedged or dying watcher can
// never leave a command holding state.
type Client struct {
	conn     net.Conn
	reader   *bufio.Reader
	deadline time.Duration
}

// Dial connects to the watcher this repository published.
//
// The pointer read is one os.ReadFile, and with no watcher running it is a
// single ENOENT. That is what keeps the unwatched path indistinguishable from
// today's.
func Dial(commonGitDir string, deadline time.Duration) (*Client, error) {
	published, err := readPointer(commonGitDir)
	if err != nil {
		return nil, ErrNoWatcher
	}
	conn, err := net.DialTimeout("unix", published.Socket, deadline)
	if err != nil {
		// A pointer surviving SIGKILL names a socket nothing answers. Falling
		// back is the whole recovery; the next watcher rebinds the same path.
		return nil, ErrNoWatcher
	}
	return &Client{conn: conn, reader: bufio.NewReader(conn), deadline: deadline}, nil
}

// Status asks what the watcher last did.
func (c *Client) Status() (Status, error) {
	answer, err := c.exchange(request{Status: &statusRequest{}})
	if err != nil {
		return Status{}, err
	}
	if answer.Status == nil {
		return Status{}, core.Errorf(core.CategoryOperational, "watcher returned no status")
	}
	return *answer.Status, nil
}

// Nudge asks the watcher to synchronize soon and waits for receipt rather than
// for completion. Receipt is what makes deferral honest: a watcher that died a
// moment ago fails here, while the caller still holds the mutation and can
// publish it inline.
func (c *Client) Nudge(taskID string) error {
	_, err := c.exchange(request{Nudge: &nudgeRequest{TaskID: taskID}})
	return err
}

// Acknowledge reports that a conflict reached a caller, so the watcher stops
// gating the task. Without it a set-membership gate would re-fire on every
// mutation and the task could never be moved forward.
func (c *Client) Acknowledge(taskID string) error {
	_, err := c.exchange(request{Ack: &ackRequest{TaskID: taskID}})
	return err
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) exchange(message request) (response, error) {
	if err := c.conn.SetDeadline(time.Now().Add(c.deadline)); err != nil {
		return response{}, core.Wrap(core.CategoryOperational, "set the watcher connection deadline", err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return response{}, core.Wrap(core.CategoryOperational, "encode the watcher request", err)
	}
	if _, err := c.conn.Write(append(encoded, '\n')); err != nil {
		return response{}, core.Wrap(core.CategoryOperational, "send the watcher request", err)
	}
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return response{}, core.Wrap(core.CategoryOperational, "read the watcher response", err)
	}
	var answer response
	if err := json.Unmarshal(line, &answer); err != nil {
		return response{}, core.Wrap(core.CategoryCorruptData, "decode the watcher response", err)
	}
	if !answer.OK {
		return response{}, core.Errorf(core.CategoryOperational, "watcher rejected the request: %s", answer.Error)
	}
	return answer, nil
}
