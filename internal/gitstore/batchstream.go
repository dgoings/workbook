package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// objectBatchWaitDelay bounds how long an abandoned batch process may keep its
// pipes open once the context that owns it is canceled.
const objectBatchWaitDelay = 5 * time.Second

// objectBatch is one long-running `git cat-file --batch` process whose request
// stream is written by a dedicated goroutine while the caller reads responses
// from the same process.
//
// Buffering a whole batch's output holds every requested object resident at
// once, which is what made full history validation's peak memory grow with the
// corpus. Streaming keeps one object resident instead. The separate writer
// goroutine is load-bearing: a single goroutine that wrote every request before
// reading any response would deadlock as soon as Git's stdout pipe filled,
// because Git stops reading stdin while it is blocked writing stdout.
type objectBatch struct {
	command *exec.Cmd
	cancel  context.CancelFunc
	reader  *bufio.Reader
	stderr  *bytes.Buffer
	args    []string

	writeErr    chan error
	writeOnce   sync.Once
	writeResult error
	waitOnce    sync.Once
	waitResult  error
}

// startObjectBatch starts `git cat-file --batch` and hands writeRequests a
// buffered writer for the request stream. writeRequests runs on its own
// goroutine and must not touch state the caller mutates while reading.
func (r *Repository) startObjectBatch(
	ctx context.Context,
	writeRequests func(io.Writer) error,
) (*objectBatch, error) {
	gitPath := r.gitPath
	if gitPath == "" {
		var err error
		gitPath, err = exec.LookPath("git")
		if err != nil {
			return nil, core.Wrap(core.CategoryOperational, "cannot find git executable", err)
		}
	}
	args := []string{"cat-file", "--batch"}
	r.observeGitCommand(args)

	batchContext, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(batchContext, gitPath, append([]string{"-C", r.Root}, args...)...)
	command.Env = gitEnvironment(os.Environ(), nil)
	command.WaitDelay = objectBatchWaitDelay
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, core.Wrap(core.CategoryOperational, "cannot open Git object batch input", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, core.Wrap(core.CategoryOperational, "cannot open Git object batch output", err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		return nil, core.Wrap(core.CategoryOperational, "cannot start Git object batch", err)
	}

	batch := &objectBatch{
		command:  command,
		cancel:   cancel,
		reader:   bufio.NewReader(stdout),
		stderr:   stderr,
		args:     args,
		writeErr: make(chan error, 1),
	}
	go func() {
		writer := bufio.NewWriter(stdin)
		err := writeRequests(writer)
		if err == nil {
			err = writer.Flush()
		}
		if closeErr := stdin.Close(); err == nil {
			err = closeErr
		}
		batch.writeErr <- err
	}()
	return batch, nil
}

// Reader returns the batch response stream. Callers read exactly the objects
// they requested, in request order.
func (b *objectBatch) Reader() *bufio.Reader {
	return b.reader
}

// Finish confirms that every requested object was consumed and that Git exited
// cleanly. Trailing output means the caller and Git disagree about the request
// stream, which makes every record in it untrustworthy.
func (b *objectBatch) Finish() error {
	if err := b.writeOutcome(); err != nil {
		b.cancel()
		return core.Wrap(core.CategoryOperational, "cannot write Git object batch requests", err)
	}
	if _, err := b.reader.ReadByte(); !errors.Is(err, io.EOF) {
		b.cancel()
		if err != nil {
			return core.Wrap(core.CategoryCorruptData, "cannot finish reading Git object batch", err)
		}
		return core.Errorf(core.CategoryCorruptData, "Git returned unexpected trailing batch data")
	}
	if err := b.wait(); err != nil {
		return core.Wrap(
			core.CategoryOperational,
			fmt.Sprintf("git %s failed", strings.Join(b.args, " ")),
			b.errorWithStderr(err),
		)
	}
	return nil
}

// Close terminates the batch and releases its writer goroutine and child
// process. It is safe on every path, including after Finish.
func (b *objectBatch) Close() {
	b.cancel()
	_ = b.writeOutcome()
	_ = b.wait()
}

func (b *objectBatch) writeOutcome() error {
	b.writeOnce.Do(func() { b.writeResult = <-b.writeErr })
	return b.writeResult
}

// wait reaps the child exactly once. Waiting before the writer goroutine has
// finished would close the request pipe underneath it, so every caller drains
// writeOutcome first.
func (b *objectBatch) wait() error {
	b.waitOnce.Do(func() { b.waitResult = b.command.Wait() })
	return b.waitResult
}

func (b *objectBatch) errorWithStderr(err error) error {
	detail := strings.TrimSpace(b.stderr.String())
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}
