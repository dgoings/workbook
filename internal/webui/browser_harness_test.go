package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
)

// A real browser, for the questions the Node harness cannot answer. That
// harness is a fake DOM with no layout engine, so it can say what the client
// built but not where any of it landed: whether the Save row is on the screen,
// or whether a sidebar scrolls. These tests serve the board from an httptest
// server, open it in headless Chrome at an exact viewport, and read the
// geometry back with a script evaluated in the page.
//
// Chrome is driven over the DevTools protocol from node, which the client
// tests already require, because --window-size alone does not decide the
// viewport: headless Chrome reserves part of the window, and a test of what
// fits in 1280x720 has to be looking at 1280x720.

// chromeCandidates are the names and install paths a Chrome or Chromium is
// found under, after WORKBOOK_TEST_CHROME.
func chromeCandidates() []string {
	candidates := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"}
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium")
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	}
	return candidates
}

// requireChrome finds a Chrome to run, or records that this machine has none.
func requireChrome(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("WORKBOOK_TEST_CHROME"); configured != "" {
		return configured
	}
	for _, candidate := range chromeCandidates() {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	testenv.MissingCapability(t, "Chrome or Chromium is required to measure the board's layout in a real browser (set WORKBOOK_TEST_CHROME to its path)")
	return ""
}

// browserPage is one page load at one viewport.
type browserPage struct {
	Width, Height int
	// Path is the board address to open, such as /tasks/<id>.
	Path string
	// Script is an expression evaluated in the page once it has loaded. It may
	// be a promise; what it resolves to is decoded into the result.
	Script string
	// Screenshot, when set, is a file a PNG of the viewport is written to
	// after the script has run.
	Screenshot string
}

// openInBrowser serves handler, loads page in headless Chrome, and decodes
// what the page's script resolved to into result.
func openInBrowser(t *testing.T, handler http.Handler, page browserPage, result any) {
	t.Helper()
	node := requireNode(t)
	chrome := requireChrome(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	profile := t.TempDir()
	arguments := []string{
		"--headless", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-background-networking", "--disable-sync",
		"--hide-scrollbars", "--remote-debugging-port=0", "--user-data-dir=" + profile,
		"--window-size=1920,1200", "about:blank",
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		// Chrome refuses to start its sandbox as root, which is how a CI
		// container usually runs.
		arguments = append([]string{"--no-sandbox"}, arguments...)
	}
	browser := exec.Command(chrome, arguments...)
	if err := browser.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	// Registered after the profile, so it runs first: the profile directory
	// cannot be removed on Windows while Chrome still holds it open.
	t.Cleanup(func() {
		_ = browser.Process.Kill()
		_ = browser.Wait()
	})

	request, err := json.Marshal(map[string]any{
		"portFile":   filepath.Join(profile, "DevToolsActivePort"),
		"url":        server.URL + page.Path,
		"width":      page.Width,
		"height":     page.Height,
		"expression": page.Script,
		"screenshot": page.Screenshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := nodeCommand(node, devtoolsDriver)
	command.Env = append(os.Environ(), "WORKBOOK_BROWSER_REQUEST="+string(request))
	output, err := command.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("measure %s at %dx%d: %v\n%s", page.Path, page.Width, page.Height, err, stderr)
	}
	if err := json.Unmarshal(output, result); err != nil {
		t.Fatalf("decode the page's answer %q: %v", strings.TrimSpace(string(output)), err)
	}
}

// devtoolsDriver is the node program that drives Chrome: it finds the port
// Chrome wrote to its profile, sets the viewport, opens the page, evaluates
// the script, and prints its value as JSON. node has had a WebSocket client
// built in since version 22.
const devtoolsDriver = `
const fs = require("fs");
const request = JSON.parse(process.env.WORKBOOK_BROWSER_REQUEST);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
(async () => {
  if (typeof WebSocket !== "function") throw new Error("this node has no WebSocket client; node 22 or later is needed");
  let port = "";
  for (let attempt = 0; attempt < 400 && !port; attempt++) {
    try { port = fs.readFileSync(request.portFile, "utf8").split("\n")[0].trim(); } catch (_) { await sleep(50); }
  }
  if (!port) throw new Error("Chrome never wrote " + request.portFile);
  let target = null;
  for (let attempt = 0; attempt < 100 && !target; attempt++) {
    try {
      const targets = await (await fetch("http://127.0.0.1:" + port + "/json/list")).json();
      target = targets.find((candidate) => candidate.type === "page");
    } catch (_) {}
    if (!target) await sleep(50);
  }
  if (!target) throw new Error("Chrome has no page to drive");
  const socket = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = () => reject(new Error("the DevTools socket did not open")); });
  let next = 0;
  const pending = new Map();
  const waiters = [];
  socket.onmessage = (event) => {
    const message = JSON.parse(event.data);
    if (message.id && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id);
      pending.delete(message.id);
      if (message.error) reject(new Error(message.error.message)); else resolve(message.result);
      return;
    }
    for (const waiter of waiters.splice(0)) {
      if (waiter.method === message.method) waiter.resolve(message.params); else waiters.push(waiter);
    }
  };
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++next;
    pending.set(id, { resolve, reject });
    socket.send(JSON.stringify({ id, method, params }));
  });
  const once = (method) => new Promise((resolve) => waiters.push({ method, resolve }));
  await send("Page.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: request.width, height: request.height, deviceScaleFactor: 1, mobile: false });
  const loaded = once("Page.loadEventFired");
  await send("Page.navigate", { url: request.url });
  await loaded;
  const evaluated = await send("Runtime.evaluate", { expression: request.expression, awaitPromise: true, returnByValue: true });
  if (evaluated.exceptionDetails) {
    const details = evaluated.exceptionDetails;
    throw new Error((details.exception && details.exception.description) || details.text);
  }
  if (request.screenshot) {
    const shot = await send("Page.captureScreenshot", { format: "png" });
    fs.writeFileSync(request.screenshot, Buffer.from(shot.data, "base64"));
  }
  process.stdout.write(JSON.stringify(evaluated.result.value === undefined ? null : evaluated.result.value));
  socket.close();
  process.exit(0);
})().catch((error) => { process.stderr.write(String(error && error.stack || error) + "\n"); process.exit(1); });
`

// pageGeometryHelpers is prepended to a page script: waitFor polls until a
// condition holds, and box reads an element's rectangle as plain numbers.
const pageGeometryHelpers = `
const waitFor = async (check, what) => {
  const deadline = Date.now() + 15000;
  while (!check()) {
    if (Date.now() > deadline) throw new Error("timed out waiting for " + what);
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
};
const box = (element) => {
  const rect = element.getBoundingClientRect();
  return { top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right, height: rect.height };
};
`
