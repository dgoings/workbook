package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// clientCommand is a client program ready to run under node, behind
// clientProgramGuard. It offers only the two ways the package's tests run one,
// and no access to the command underneath: a test cannot swap the program on
// its standard input, its arguments or the binary after the guard has been put
// in front of it, because there is nothing to assign to.
type clientCommand struct {
	command *exec.Cmd
	// marker is the line the guard writes once the program has reached its own
	// end. It is fresh for every command, so no program can print it by
	// accident or by copying it from another test.
	marker string
}

// nodeCommand feeds the program to node over stdin rather than as an argument.
// The client programs embed the page's whole script, and Linux caps one argv
// string at 128 KiB (MAX_ARG_STRLEN), a ceiling the largest programs have
// outgrown; stdin has no such limit and behaves identically on every platform
// CI runs.
//
// It also puts clientProgramGuard in front of every program, so a program
// that does not reach its own end fails rather than exiting cleanly.
func nodeCommand(node, program string) *clientCommand {
	return nodeCommandContext(context.Background(), node, program)
}

// nodeCommandContext is nodeCommand for a program that needs a hang guard of
// its own: the command is killed when ctx is done. It is the one place in this
// package that starts node for a client program, which
// TestEveryClientProgramStartsBehindTheGuard holds every test to.
func nodeCommandContext(ctx context.Context, node, program string) *clientCommand {
	marker := clientProgramMarker()
	command := exec.CommandContext(ctx, node, "-")
	command.Stdin = strings.NewReader(guardedClientProgram(marker, program))
	return &clientCommand{command: command, marker: marker}
}

// CombinedOutput runs the program and returns what it wrote to stdout and
// stderr together, as exec.Cmd.CombinedOutput does, with the guard's marker
// taken out. A program that exited 0 without writing the marker is an error.
func (client *clientCommand) CombinedOutput() ([]byte, error) {
	var output bytes.Buffer
	client.command.Stdout = &output
	client.command.Stderr = &output
	err := client.command.Run()
	return client.finish(output.Bytes(), err)
}

// Output runs the program and returns what it wrote to stdout, as
// exec.Cmd.Output does. The marker is written to stderr, so stdout is the
// program's own; stderr is kept for the error a failed run returns.
func (client *clientCommand) Output() ([]byte, error) {
	var stdout, stderr bytes.Buffer
	client.command.Stdout = &stdout
	client.command.Stderr = &stderr
	err := client.command.Run()
	said, err := client.finish(stderr.Bytes(), err)
	if err != nil && len(said) > 0 {
		err = fmt.Errorf("%w\n%s", err, said)
	}
	return stdout.Bytes(), err
}

func (client *clientCommand) finish(output []byte, err error) ([]byte, error) {
	line := []byte(client.marker + "\n")
	reached := bytes.Contains(output, line)
	output = bytes.ReplaceAll(output, line, nil)
	if err == nil && !reached {
		err = fmt.Errorf("client program exited 0 without reaching its own end: clientProgramGuard never saw it finish")
	}
	return output, err
}

var markerSequence struct {
	sync.Mutex
	next int
}

func clientProgramMarker() string {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on; a counter
		// still keeps markers distinct if it ever did.
		markerSequence.Lock()
		markerSequence.next++
		nonce[0] = byte(markerSequence.next)
		markerSequence.Unlock()
	}
	return "workbook-client-program-reached-its-end-" + hex.EncodeToString(nonce[:])
}

// pageScriptBounds are the first and last lines of the page's own script, read
// from the assets the page is composed from: boot.js opens the one function
// the script is, and events.js closes it. A program embeds the script a page
// served, so these find it there, and the guard can tell the page's code from
// the program's checks without the program saying which is which.
var pageScriptBounds = sync.OnceValues(func() (string, string) {
	head, err := assets.ReadFile("assets/script/boot.js")
	if err != nil {
		panic("webui: read boot.js for the client program guard: " + err.Error())
	}
	tail, err := assets.ReadFile("assets/script/events.js")
	if err != nil {
		panic("webui: read events.js for the client program guard: " + err.Error())
	}
	first := strings.TrimRight(string(head), "\n")
	if at := strings.Index(first, "\n"); at >= 0 {
		first = first[:at]
	}
	// The opening line alone, "(() => {", is too common to find the script by,
	// so the head is its first three lines.
	lines := strings.SplitN(string(head), "\n", 4)
	if len(lines) >= 3 {
		first = strings.Join(lines[:3], "\n")
	}
	last := strings.TrimRight(string(tail), "\n")
	if at := strings.LastIndex(last, "\n"); at >= 0 {
		// The closing "})();" and the line before it, for the same reason.
		before := strings.LastIndex(last[:at], "\n")
		last = last[before+1:]
	}
	return first, last
})

// pageScriptLines is the 1-based line range the page's script occupies in
// program, or 0, 0 when program does not embed it.
func pageScriptLines(program string) (int, int) {
	head, tail := pageScriptBounds()
	start := strings.Index(program, head)
	end := strings.LastIndex(program, tail)
	if start < 0 || end < start {
		return 0, 0
	}
	end += len(tail)
	return strings.Count(program[:start], "\n") + 1, strings.Count(program[:end], "\n") + 1
}

// guardedClientProgram is program as node runs it: the guard on the first
// line, then the program, then the line that says the program's top level ran
// to its end.
func guardedClientProgram(marker, program string) string {
	first, last := pageScriptLines(program)
	if first > 0 {
		// The guard takes the first line, so every program line is one lower.
		first, last = first+1, last+1
	}
	guard := strings.NewReplacer(
		"__MARKER__", strconv.Quote(marker),
		"__PAGE_FIRST__", strconv.Itoa(first),
		"__PAGE_LAST__", strconv.Itoa(last),
	).Replace(clientProgramGuard)
	return guard + "\n" + program + "\n;globalThis.clientProgramReachedItsEnd();\n"
}

// clientProgramGuard makes a client program prove it reached its own end.
//
// Node exits cleanly once nothing is left to run, so a check that awaits a
// promise nobody ever settles does not fail: the event loop runs dry with the
// rest of the checks unrun, and the program exits 0. So exiting 0 is not
// taken as success. A program succeeds only when the guard writes a marker
// that nodeCommand's caller requires to see, and the guard writes it only
// when node has run out of work and every one of these holds:
//
//   - The program's top level ran to its last line. The harness appends a
//     line after the program that records this, so a program cut short — a
//     top-level return, a process.exit before the end — never records it.
//   - Every callback the program handed to node's own scheduling — setTimeout,
//     setImmediate, queueMicrotask and process.nextTick, through the globals
//     or require("timers") — has returned, and every promise one returned has
//     settled. clientProgram(async () => ...) is followed the same way.
//   - Every promise the program's checks created has settled, unless the
//     page's own code is waiting on it. A promise counts as the checks' when
//     the code creating it is the program's and none of the page's script is
//     on the stack at that moment; the page's script is found in the program
//     by the first lines of boot.js and the last lines of events.js. A promise
//     the page creates is the page's business: a test holds a request open by
//     handing the page one that never answers, and that is not a stalled
//     check. For the same reason a check-made promise the page itself awaits
//     or chains from is the page's to wait on, and is not counted.
//
// process.exit is intercepted: exiting 0 through it is judged the same way,
// at that moment, so a program that exits from inside a check that has not
// finished fails. An exit that names a failure passes through untouched. A
// check that throws fails as it always did, as an unhandled rejection.
//
// What no harness can see, and this does not claim to: a check whose failure
// is caught and dropped (a catch that swallows the error), and a program with
// no checks in it at all. Both reach their end, and reaching the end is all a
// harness can ask of a program; that a program checks anything is for review.
//
// It is one line, so the line numbers in a failing program's stack are off by
// exactly one. It is written in Go below with no line comments, because it is
// joined onto that one line.
var clientProgramGuard = strings.Join(strings.Fields(`(() => {
  const fs = require("fs");
  const timers = require("timers");
  const asyncHooks = require("async_hooks");
  const marker = __MARKER__;
  const pageFirst = __PAGE_FIRST__;
  const pageLast = __PAGE_LAST__;
  const state = { reached: false, unfinished: 0, said: false, checks: new Set(), awaited: new Set() };
  const say = (line) => { try { fs.writeSync(2, line + "\n"); } catch (_) {} };
  const realExit = process.exit.bind(process);
  const origin = () => {
    const limit = Error.stackTraceLimit;
    const prepare = Error.prepareStackTrace;
    Error.stackTraceLimit = 60;
    Error.prepareStackTrace = (_, sites) => sites;
    const holder = {};
    Error.captureStackTrace(holder);
    const sites = holder.stack;
    Error.prepareStackTrace = prepare;
    Error.stackTraceLimit = limit;
    let check = false;
    for (const site of sites) {
      if (site.getFileName() !== "[stdin]") continue;
      const line = site.getLineNumber();
      if (line === 1) continue;
      if (pageFirst > 0 && line >= pageFirst && line <= pageLast) return "page";
      check = true;
    }
    return check ? "check" : "other";
  };
  asyncHooks.createHook({
    init(id, type, trigger) {
      if (type !== "PROMISE" || state.said) return;
      const from = origin();
      if (from === "check") state.checks.add(id);
      else if (from === "page" && state.checks.has(trigger)) state.awaited.add(trigger);
    },
    promiseResolve(id) { state.checks.delete(id); }
  }).enable();
  const verdict = () => {
    if (!state.reached) return "client program ended before its last line ran";
    if (state.unfinished > 0) return "client program ended with " + state.unfinished + " of its checks unfinished: something they awaited never settled";
    let stuck = 0;
    for (const id of state.checks) if (!state.awaited.has(id)) stuck += 1;
    if (stuck > 0) return "client program ended with " + stuck + " promise(s) its checks started still unsettled: something they awaited never settled";
    return "";
  };
  const conclude = () => {
    if (state.said) return true;
    const reason = verdict();
    state.said = true;
    if (reason) { say(reason); return false; }
    say(marker);
    return true;
  };
  const follow = (result) => {
    if (!result || typeof result.then !== "function") return result;
    state.unfinished += 1;
    result.then(() => { state.unfinished -= 1; }, (error) => { state.unfinished -= 1; throw error; });
    return result;
  };
  const tracked = (callback) => function (...args) {
    state.unfinished += 1;
    try { return follow(callback.apply(this, args)); } finally { state.unfinished -= 1; }
  };
  const wrap = (owner, name) => {
    const real = owner[name];
    if (typeof real !== "function") return;
    const guarded = function (callback, ...rest) {
      if (typeof callback !== "function") return real.call(this, callback, ...rest);
      return real.call(this, tracked(callback), ...rest);
    };
    const custom = require("util").promisify.custom;
    if (real[custom]) guarded[custom] = real[custom];
    owner[name] = guarded;
  };
  for (const name of ["setTimeout", "setImmediate", "queueMicrotask"]) wrap(globalThis, name);
  for (const name of ["setTimeout", "setImmediate"]) wrap(timers, name);
  wrap(process, "nextTick");
  globalThis.clientProgram = (body) => follow(body());
  globalThis.clientProgramReachedItsEnd = () => { state.reached = true; };
  process.exit = (code) => {
    const status = code === undefined ? (process.exitCode || 0) : code;
    if (Number(status) !== 0) return realExit(code);
    if (!state.said && verdict()) say("client program called process.exit(0) before it was finished:");
    return realExit(conclude() ? 0 : 1);
  };
  process.on("beforeExit", () => { if (!conclude()) process.exitCode = 1; });
  process.on("exit", (code) => {
    if (code === 0 && !state.said) {
      say("client program exited without reaching its own end");
      process.exitCode = 1;
    }
  });
})();`), " ")
