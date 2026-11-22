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
- `workbook` installed on it; see [Installation](../README.md#installation).
- The project's repository on GitHub, already holding a Workbook project: any
  clone that has run `workbook setup` and `workbook sync` has published one.
- A domain on Cloudflare. Tunnel publishes an application under a domain you
  have [added to Cloudflare](https://developers.cloudflare.com/fundamentals/manage-domains/add-site/).
- A Cloudflare Zero Trust organization. The Free plan covers up to 50 users at
  no cost ([plans](https://www.cloudflare.com/plans/zero-trust-services/)). A
  person takes a seat the first time they sign in to an Access application
  ([seat management](https://developers.cloudflare.com/cloudflare-one/team-and-resources/users/seat-management/)).

The examples use `tasks.example.com` for the board's address,
`OWNER/REPO` for the repository, and `board@example.com` for the server's own
address. Replace each with yours.

## 1. A user and a folder for the board

Run the board as its own system user, so its key and its folder belong to
nothing else:

```sh
sudo useradd --system --create-home --home-dir /var/lib/workbook --shell /usr/sbin/nologin workbook
```

## 2. A deploy key with write access

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

## 3. The tasks-only folder

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

## 4. Run the board as a service

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

Use the path `command -v workbook` prints for `ExecStart`. Then:

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
```

`--addr 127.0.0.1:7331` is what keeps the board unreachable from anywhere but
this machine. Do not change it to `0.0.0.0`. See
[The board trusts the name header](#the-board-trusts-the-name-header).

The board is already refusing every request that does not carry the header,
except its health check:

```sh
curl -s http://127.0.0.1:7331/healthz   # answers
curl -s http://127.0.0.1:7331/api/tasks # refused with 401
```

## 5. An Access application with one-time PIN sign-in

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

## 6. A Cloudflare Tunnel

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

## 7. Check what the example promises

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
without a valid Access token. Step 7 shows how to check both. It follows that
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

Whoever can read that log can read those details: on most systems, root and
members of the `adm` or `systemd-journal` groups.

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

Nothing in the board limits how much one signed-in person can send: how many
tasks, comments or attachments, or how fast. One person, or one script with
their session, can fill the server's disk and the repository and slow the
board for everyone. These are the limits Cloudflare itself applies on the Free
plan, which are all there is in front of the board:

- A request body over 100 MB is refused with `413`
  ([upload limits](https://developers.cloudflare.com/cache/concepts/default-cache-behavior/#upload-limits)).
  The board's own attachment ceiling is far below that, so this never decides
  anything.
- Cloudflare waits 125 seconds for the board to answer before it answers `524`
  ([connection limits](https://developers.cloudflare.com/fundamentals/reference/connection-limits/)).
- A Free plan zone can have one rate limiting rule, counting requests by IP
  address over 10 seconds and blocking for 10 seconds
  ([rate limiting rules](https://developers.cloudflare.com/waf/rate-limiting-rules/#availability),
  [an example on the Free plan](https://developers.cloudflare.com/use-cases/solutions/protect-sensitive-forms-fraud-abuse/)).

If you add that one rule, match only the writes, such as requests whose path
starts with `/api/` and whose method is not `GET`, and set a threshold well
above what a person clicking produces. The board's page polls once a second,
so a rule that counts reads will block people for doing nothing. Remember that
people behind one office's network share an IP address and share the limit.
When the board's page is refused with `429` and a `Retry-After` header, it
waits as long as the header says before trying again.

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
