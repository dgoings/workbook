# Hosting the board

This guide puts one Workbook board on the internet for a small team, behind a
sign-in page, so that each person's edits are recorded under their own email
address. It is written for somebody doing this for the first time. It uses:

- a Linux machine that keeps its disk across restarts;
- a [tasks-only folder](reference.md#tasks-only-folders) holding the project's
  tasks and none of its code;
- a GitHub deploy key with write access, so the board can publish;
- `workbook serve` listening on this machine only;
- [Cloudflare Tunnel](https://developers.cloudflare.com/tunnel/), so nothing
  on the machine has to accept connections from the internet;
- [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/access-controls/policies/)
  with one-time PIN sign-in: each person types their email address and gets a
  code by email.

Cloudflare's dashboard and plans change. Each step below names the Cloudflare
or GitHub page it follows, so you can check it against what you see. Read
[What to know before you invite anyone](#what-to-know-before-you-invite-anyone)
before you share the address.

## What you need

- A Linux machine with a disk that survives a restart, such as a small virtual
  machine with an ordinary persistent disk, and a shell on it with `sudo`.
- Git 2.29 or later on it. Ubuntu 24.04 ships Git 2.43.
- The project's repository on GitHub, already holding a Workbook project: any
  clone that has run `workbook setup` and `workbook sync` has published one.
- A domain on Cloudflare. Tunnel publishes an application under a domain you
  have [added to Cloudflare](https://developers.cloudflare.com/fundamentals/manage-domains/add-site/).
- A Cloudflare Zero Trust organization. Check the current plan limits, including
  how many people the Free plan admits
  ([plans](https://www.cloudflare.com/plans/zero-trust-services/)). A person
  takes a seat the first time they sign in, and once every seat is taken,
  anybody new who signs in is blocked
  ([seat management](https://developers.cloudflare.com/cloudflare-one/team-and-resources/users/seat-management/)).

The examples use `tasks.example.com` for the board's address,
`OWNER/REPO` for the repository, and `board@example.com` for the server's own
address. Replace each with yours.

## 1. Install Workbook

The board needs a Workbook release whose `workbook serve --help` lists
`--public-url` and `--actor-header`, and whose `workbook setup --help` lists
`--tasks-only`. v0.6.1, the newest release when this guide was written, has
none of them, so until a release does, build from source.

Install it in `/usr/local/bin`, where the service below runs it from and where
`sudo` finds it. Both ways below need Git, SSH and curl; on Ubuntu or Debian:

```sh
sudo apt-get install -y git openssh-client curl ca-certificates
```

From a release:

```sh
version=X.Y.Z          # the release, without its leading v
arch=$(dpkg --print-architecture)   # amd64 or arm64
cd "$(mktemp -d)"
curl -fsSLO "https://github.com/dgoings/workbook/releases/download/v${version}/workbook_${version}_linux_${arch}.tar.gz"
curl -fsSLO "https://github.com/dgoings/workbook/releases/download/v${version}/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "workbook_${version}_linux_${arch}.tar.gz" workbook
sudo install -m 0755 workbook /usr/local/bin/workbook
```

`sha256sum` must print `OK` for the archive before you go on.

From source, with Go 1.26 or later from [go.dev/dl](https://go.dev/dl/)
(Ubuntu 24.04's own `golang` package is older):

```sh
git clone https://github.com/dgoings/workbook.git
cd workbook
./scripts/install.sh "$PWD/bin"
sudo install -m 0755 bin/workbook /usr/local/bin/workbook
```

`scripts/install.sh` builds into the folder it is given; it prints an
`export PATH=…` line when that folder is not on your `PATH`, which you can
ignore here. Build as yourself and copy the result, rather than running the
script with `sudo`, because `sudo` usually does not find Go.

## 2. A user and a folder for the board

Run the board as its own system user, so its key and its folder belong to
nothing else:

```sh
sudo useradd --system --create-home --home-dir /var/lib/workbook --shell /usr/sbin/nologin workbook
sudo -u workbook workbook version
```

The second command must print the version you installed. If it says the
command was not found, `workbook` is not in `/usr/local/bin`.

## 3. A deploy key with write access

The board publishes every change to the repository, so it needs a key that can
push. A [deploy key](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/managing-deploy-keys#deploy-keys)
is an SSH key attached to one repository rather than to a person.

Make the key on the server, with no passphrase so the service can use it
unattended:

```sh
sudo -u workbook mkdir -p -m 700 /var/lib/workbook/.ssh
sudo -u workbook ssh-keygen -t ed25519 -N "" -C "workbook board" \
  -f /var/lib/workbook/.ssh/board_deploy_key
sudo cat /var/lib/workbook/.ssh/board_deploy_key.pub
```

On GitHub, open the repository's **Settings**, then **Deploy keys**, then
**Add deploy key**. Give it a title, paste the public key, select **Allow
write access**, and select **Add key**.

Tell SSH to use that key for GitHub, and record GitHub's host key:

```sh
sudo -u workbook tee /var/lib/workbook/.ssh/config >/dev/null <<'EOF'
Host github.com
  IdentityFile /var/lib/workbook/.ssh/board_deploy_key
  IdentitiesOnly yes
EOF
sudo -u workbook ssh -T git@github.com
```

The first connection asks whether to trust GitHub's host key. Answer yes only
if the fingerprint it shows is one of
[GitHub's published fingerprints](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints).

Read [The server's key opens the whole repository](#the-servers-key-opens-the-whole-repository)
before you go on.

## 4. The tasks-only folder

```sh
sudo -u workbook -H sh -c '
  mkdir /var/lib/workbook/board && cd /var/lib/workbook/board &&
  workbook setup --tasks-only git@github.com:OWNER/REPO.git &&
  git config user.email board@example.com
'
```

The folder holds the project's Workbook refs and none of its code. The
`user.email` is the server's own name. With the setup below, people's edits
are recorded under their own names, not this one, but `workbook serve` still
needs a name of its own to start.

## 5. Run the board as a service

Write `/etc/systemd/system/workbook-board.service`:

```ini
[Unit]
Description=Workbook board
After=network-online.target
Wants=network-online.target

[Service]
User=workbook
WorkingDirectory=/var/lib/workbook/board
Environment=WORKBOOK_SERVE_PUBLIC_URL=https://tasks.example.com
Environment=WORKBOOK_SERVE_ACTOR_HEADER=Cf-Access-Authenticated-User-Email
ExecStart=/usr/local/bin/workbook serve --addr 127.0.0.1:7331
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`ExecStart` names `/usr/local/bin/workbook`, where step 1 installed it.
Then:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now workbook-board
sudo journalctl -u workbook-board -n 20
```

The log should show:

```text
Workbook board: http://127.0.0.1:7331
Public address: https://tasks.example.com
Each request's person: the Cf-Access-Authenticated-User-Email header
Workbook sync watcher: 5s interval, socket /tmp/workbook-…/wb-….sock
```

The last line is the board's background sync starting.

`--addr 127.0.0.1:7331` is what keeps the board unreachable from anywhere but
this machine. Do not change it to `0.0.0.0`. See
[The board trusts the name header](#the-board-trusts-the-name-header).

The board is already refusing every request that does not carry the header,
except its health check:

```sh
curl -s http://127.0.0.1:7331/healthz   # answers
curl -s http://127.0.0.1:7331/api/tasks # refused with 401
```

## 6. An Access application with one-time PIN sign-in

Create the sign-in before the tunnel's route, so the board is never on the
internet without it. Cloudflare's guide says the same: a published application
with no Access application in front of it is available to anyone
([self-hosted applications](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/self-hosted-public-app/)).

Turn on one-time PIN sign-in. New Zero Trust organizations no longer add it on
their own ([one-time PIN login](https://developers.cloudflare.com/cloudflare-one/integrations/identity-providers/one-time-pin/)):

1. In the Cloudflare dashboard, go to **Zero Trust** > **Integrations** >
   **Identity providers**.
2. Under **Your identity providers**, select **Add new identity provider**.
3. Select **One-time PIN**.

Then create the application
([self-hosted applications](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/self-hosted-public-app/)):

1. Go to **Zero Trust** > **Access controls** > **Applications**, and select
   **Create new application**.
2. Select **Self-hosted and private**, then **Add public hostname**, and enter
   `tasks.example.com`.
3. Under **Access policies**, create a policy with the action **Allow**. Put
   each teammate's email address in an **Include** rule with the selector
   **Emails**, and add a **Require** rule with the selector **Login methods**
   set to **One-time PIN**. Include rules are joined by OR, so a login method
   placed in Include would let in anybody who signs in that way
   ([common policies](https://developers.cloudflare.com/cloudflare-one/access-controls/policies/common-policies/)).
4. Under the authentication settings, select **One-time PIN** as the identity
   provider.
5. Choose a **Session Duration**, and select **Create**.

Every Access application denies by default: only somebody an Allow policy
names gets a code. A blocked address is told a code was sent and gets none
([one-time PIN login](https://developers.cloudflare.com/cloudflare-one/integrations/identity-providers/one-time-pin/)).
A code expires ten minutes after it is requested.

Copy the application's **Application Audience (AUD) Tag**: edit the
application and find it under **Additional settings**
([get your AUD tag](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/#get-your-aud-tag)).
You need it in the next step, with your team name.

## 7. A Cloudflare Tunnel

Create a remotely managed tunnel and install `cloudflared` as a service
([create a tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/)):

1. In the Cloudflare dashboard, go to **Networking** > **Tunnels**, and select
   **Create a tunnel**.
2. Name it, such as `workbook-board`, and select **Create Tunnel**.
3. Choose your server's operating system, copy the installation command the
   dashboard shows, and run it on the server. It installs `cloudflared` and
   runs it as a system service with the tunnel's token (`sudo cloudflared
   service install <token>`).
4. Wait for the tunnel to connect, and select **Continue**.

Publish the board through it:

1. On the tunnel's **Routes** tab, select **Add route**, then **Published
   application**.
2. Enter the subdomain `tasks` and choose your domain.
3. For **Service URL**, enter `http://127.0.0.1:7331`. Use the address rather
   than `localhost`, which can resolve to the IPv6 loopback the board does not
   listen on.
4. Expand **Additional application settings**, find **Access**, and turn on
   **Protect with Access**. Enter your team name and the AUD tag you copied.
   `cloudflared` then checks the signed token Access attaches to every request
   and refuses a request without a valid one before it reaches the board
   ([origin parameters: access](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/origin-parameters/#access)).
5. Select **Add route**.

Open `https://tasks.example.com`. You should see Cloudflare's sign-in page,
then, after the code, the board. Make a change, and its history names you:

```sh
workbook show <task-id> --history
```

## 8. Check what the example promises

Run these before you invite anyone.

From another machine, the board's port does not answer:

```sh
curl -m 5 http://<server-address>:7331/healthz   # must fail to connect
```

On the server, the board listens on the loopback address only:

```sh
sudo ss -ltnp | grep 7331   # shows 127.0.0.1:7331 and nothing else
```

Signed in, you cannot name somebody else. Copy the `CF_Authorization` cookie
from your browser's developer tools after signing in, and send the header
yourself:

```sh
curl -s https://tasks.example.com/api/tasks \
  --cookie "CF_Authorization=<your cookie>" \
  -H "Cf-Access-Authenticated-User-Email: someone-else@example.com" | head -c 300
```

The board must either refuse it with `400` because the header arrived twice,
or answer as you. Then create a task the same way, with
`-X POST -H "Content-Type: application/json" -H "Origin: https://tasks.example.com" --data '{"title":"Header check"}'`,
and check that its history names you and not `someone-else@example.com`.
Delete the task afterwards. If it names somebody else, Access passed your
header through: do not use the board this way, and stop the service until you
know why. Cloudflare's documentation does not say what Access does with an
email header a visitor sends, which is why this check is here.

## What to know before you invite anyone

### The server's key opens the whole repository

GitHub says a deploy key with write access can do what an organization member
with admin access, or a collaborator on a personal repository, can do
([deploy keys](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/managing-deploy-keys#deploy-keys)).
It is not limited to Workbook's refs. Whoever reads
`/var/lib/workbook/.ssh/board_deploy_key` can push to every branch and tag of
that repository, from anywhere, until the key is removed. The key has no
passphrase and no expiry date. Keep the machine's other users and services away
from that folder, and remove the key on GitHub the day the board is retired.

The exposure comes from the tasks living in the same repository as the code.
Keeping them in a repository of their own, which holds tasks and nothing else,
would leave the key nothing else to open, but every clone shares tasks through
its `origin`, so this version cannot. Sharing tasks through a remote other than
`origin` is planned, not available.

### The board trusts the name header

The board takes each person's name from the `Cf-Access-Authenticated-User-Email`
header and does not check that the request came through Access. Cloudflare's
own guidance is to validate the signed `Cf-Access-Jwt-Assertion` token rather
than trust an unverified email header
([identity-gated edge](https://developers.cloudflare.com/reference-architecture/design-guides/identity-gated-edge/)).
This version does not. Anybody who can reach the board's port without going
through Access can send the header and act as anyone.

The example closes that two ways. The board listens on `127.0.0.1`, so only
programs on the server can reach it, and `cloudflared` runs on the same
machine. **Protect with Access** makes `cloudflared` itself refuse a request
without a valid Access token. Step 8 shows how to check both. It follows that
everybody with a shell on the server can act as anyone on the board.
`workbook serve` prints a warning when the header is on and it listens beyond
the machine.

### A proxy name must equal `user.email` exactly

The board records the name exactly as Access sends it, case included. A person
who also works from their own clone is the same person to Workbook only if
their `user.email` there is the address they sign in with, in the same case:
`Sam@example.com` and `sam@example.com` are two people. A comment they wrote in
one place is not theirs to edit in the other, and an assignment held under one
is not withdrawn by the other. Ask everybody to sign in with the address their
`git config user.email` prints.

### A save and its publication are a moment apart

When the board answers a save, the change is written on the server's disk. It
reaches GitHub a moment later, normally within a few seconds, when the board's
background sync publishes it. Until then it exists only on the server. A
machine that loses its disk in that moment loses that change, and nobody else
has it.

That is why this guide asks for a machine that keeps its disk. A host that
wipes the disk on every restart or deploy, such as a container with no
persistent volume, loses every change not yet published each time it restarts,
and starts the next time from whatever GitHub holds. Use a virtual machine with
an ordinary disk, or give the container a persistent volume for
`/var/lib/workbook`.

### A removed or edited comment's text stays in the history

Removing a comment hides it from the thread, and editing one shows the new
text, but the task's history keeps every earlier version. Anybody who can open
the board can read it, and so can anybody with a clone. Nothing on the board
erases text once it was saved.

### The server's log holds what answers leave out

Because the board is started with a public address, it keeps Git's output, the
server's folder and the remote's address out of what it answers. A person
whose change hit a server problem sees a short sentence and a reference such
as `3f9a02c41b7e`, and the server's log has the whole message under it:

```sh
sudo journalctl -u workbook-board | grep 3f9a02c41b7e
```

The log also has a line for each failure of the board's background sync, the
round every few seconds that fetches and publishes. It prints Git's own
message, with any credential in the remote's address removed, once when a
failure starts, again only when the message changes, and once when a round
succeeds again:

```text
workbook: background sync failed: git push failed: … fatal: Could not read from remote repository.
workbook: background sync is working again
```

So a board that has stopped publishing says so once, not every few seconds,
and a single line after it says it recovered. Two more kinds of line belong
here: `workbook: conflict on <task>: …` when the sync sets an edit aside (see
[Edits the board's sync sets aside](#edits-the-boards-sync-sets-aside)), and
`workbook serve: <person> dismissed the set-aside edits of <task> held at <ref>
(commit <commit>)` when somebody dismisses one.

Whoever can read that log can read those details: on most systems, root and
members of the `adm` or `systemd-journal` groups.

### Edits the board's sync sets aside

Sometimes a change saved on the board and a change on its way to GitHub from
somebody's clone cannot both stand: the same description rewritten two ways,
an edit to a task the other side deleted, or a dependency that would close a
loop with one the other side added. Whichever reaches GitHub first wins. When
the clone's wins, the board's background sync keeps it, and sets aside the
board's edit, and every later edit to the same task saved on the board before
the sync ran, rather than apply them over it. Nothing is lost at that moment:
the set-aside edits are kept in the server's folder.

Everybody who opens the board sees them, and goes on seeing them after the
service restarts:

- The task's card says **1 edit set aside**, or how many.
- The task's page shows a notice above the form: who made each set-aside edit
  and when, which field it changed, and the text it would have saved, with a
  **Copy text** button beside a description or a comment.

To deal with one, either:

- copy back what should be kept and save it. Once the task carries what an
  edit would have done, the board stops showing that edit; or
- select **Dismiss this edit** (or **Dismiss these edits**) once nobody needs
  them. Anybody signed in can dismiss, and the log records who did.

The command line has no way to list or dismiss set-aside edits; a person
working from their own clone is told of a conflict once, when their own sync
meets it. The server keeps a copy each time its sync rearranges a task's
history, and only the three newest per task, so an edit nobody dealt with is
dropped once the same task has been rearranged three more times. Compacting a
task drops its copies too.

### Do not name a service account with an ordinary word

Workbook removes credentials from Git's messages before showing them. When a
remote's address carries a user name and no password, as in
`https://<user>@host/OWNER/REPO.git`, a user name of eight characters or more
is removed wherever it appears in Git's output, because such a user name is
often the token itself. A user name like `taskboard` would therefore be
blanked from every message that mentions the word. This guide's SSH address,
`git@github.com:OWNER/REPO.git`, carries no such user name. If you use an
address that does, give it a name that never appears as an ordinary word.

### Invisible characters are drawn as the board's docs describe

A title or a file name can carry characters that draw as nothing, or that
reverse the text after them. The board draws direction overrides and isolates
as spaces, and in file names it draws blank-looking fillers as one space, as
[the reference](reference.md#local-web-board) describes. It leaves three
direction marks in place (U+200E, U+200F and U+061C), because Hebrew and
Arabic writers rely on them; marks placed between digits can still make
`10.99` read `99.10`. Read an amount or a date that matters with
`workbook show`, which prints the marks as spaces.

### The board has no per-person limit

Nothing in the board counts what one signed-in person sends: how many tasks,
comments or attachments, or how fast. One person, or one script with their
session, can fill the server's disk and the repository, and can stall the
board for everyone, by flooding it with requests or by holding many slow
connections open. These are the limits the board itself has, each per request
or per task, never per person:

- A request's headers must arrive within 10 seconds, the whole request within
  30 seconds, and an idle connection is closed after 2 minutes. There is no
  limit on how long an answer may take to send, because a save may wait on a
  push to GitHub.
- A request body may be 1 MiB, except an attachment upload, which may be about
  1.4 MiB because the file travels base64-encoded.
- One attached file may be 1 MiB, a task's live attached files 10 MiB together,
  and a task 50 attachments, 500 comments of up to 16 KiB each, and a
  description of up to 64 KiB.

These are the limits Cloudflare applies on the Free plan, which are all there
is in front of the board:

- An upload may be 100 MB
  ([upload limits](https://developers.cloudflare.com/cache/concepts/default-cache-behavior/#upload-limits)).
  The board's own ceilings are far below that, so this never decides anything.
- Cloudflare waits 125 seconds for the board to answer before it answers `524`
  ([connection limits](https://developers.cloudflare.com/fundamentals/reference/connection-limits/)).
- A Free plan zone can have one rate limiting rule. It counts requests by IP
  address only, over 10 seconds, and blocks for 10 seconds
  ([rate limiting rules](https://developers.cloudflare.com/waf/rate-limiting-rules/#availability)).
  Counting by a header or a cookie, which is what would count per person,
  needs an Enterprise plan.

So on the Free plan you can limit an address, not a person. If you add that
one rule, match only the writes, such as requests whose path starts with
`/api/` and whose method is not `GET`, and set a threshold well above what a
person clicking produces. The board's page polls once a second, so a rule that
counts reads will block people for doing nothing. People behind one office's
network share an address and share the limit, and one person on several
networks gets the limit several times. When the board's page is refused with
`429` and a `Retry-After` header, it waits as long as the header says before
trying again. Beyond that, the remedy for somebody abusing the board is to
remove them from the Access policy and, if they filled the repository, to
compact or restore it by hand.

### Everyone signed in can do everything

The board has no roles. Everybody the Access policy lets in can, among other
things:

- delete any task, and restore it;
- remove anybody's comment and anybody's attachment (only a comment's author
  can edit it);
- assign anybody to any task;
- add, rename, reorder and remove statuses and priorities, rename and recolor
  the board, and add project keys or change which one new tasks are numbered
  under (a key cannot be renamed);
- dismiss edits the sync set aside, which deletes the server's copy of them;
- flip the board's **Push** switch, which decides for everybody whether a save
  waits for its push to GitHub or leaves it to the background sync, until the
  service restarts.

Every one of these is recorded, under the name of whoever did it, except the
**Push** switch, which records nothing. The
[reference](reference.md#what-anyone-who-can-open-the-board-can-do-and-see)
lists all of it. Let in only people you would trust with all of it.

### Running the command line beside the board

The board already synchronizes the folder every few seconds, so there is no
need to run `workbook sync` in it. Running commands there anyway is safe but
can be refused while the board is in the middle of its own sync. In a test of
80 rounds, each running a board edit, a command-line edit and a `workbook sync`
in the server's folder at once, plus an edit and sync from another clone, 10 of
the 320 were refused and nothing was lost: every one of the 238 accepted
changes reached the remote, and the refused ones wrote nothing. The refusals
were:

- `workbook sync`: `canonical task refs changed during fetch` (exit `6`). Git's
  own message beneath it reads `cannot lock ref … reference already exists`,
  because the board fetched the same task first.
- `workbook sync`: `1 task ref(s) were rejected by origin` (exit `1`), because
  the board published first. The next sync replays the change.
- `workbook create`: `cannot refresh projection cache after a concurrent
  update` (exit `1`). Nothing was recorded.

Run the command again. Prefer the board, or your own clone, for edits.

## Updating and retiring

To update Workbook, install the new version and restart the service with
`sudo systemctl restart workbook-board`. On a stop, `workbook serve` finishes
its last synchronization before it exits, so changes saved just before the
restart are published.

To retire the board, stop and disable the service, delete the tunnel and the
Access application in the Cloudflare dashboard, and remove the deploy key in
the repository's settings on GitHub.
