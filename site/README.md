# Workbook site

The Workbook marketing page. One hand-written `index.html` with its CSS and its
copy-to-clipboard script inline — no framework, no build step, no external
requests. Open `site/index.html` in a browser to see exactly what Render
serves, or run a local server if you want the real path handling:

```sh
python3 -m http.server 8000 --directory site
```

## Hosting on Render

[`render.yaml`](../render.yaml) at the repository root is a Render Blueprint
describing one static service: root directory `site`, no build command, publish
directory `.`, plus response headers and a build filter that skips deploys for
pushes that only touch the Go program.

To create the service in a Render workspace:

1. In the workspace, choose **New → Blueprint** and connect the
   `dgoings/workbook` repository. Render reads `render.yaml` from the branch you
   select — pick `main` once this has merged.
2. Review the plan it shows. It should propose a single static site named
   `workbook-site`. Static sites are free on Render, so no instance type is
   asked for.
3. Apply it. The first deploy takes well under a minute, and the site is live at
   `workbook-site.onrender.com` (Render appends a suffix if that name is taken;
   the service's real URL is on its dashboard page).

Afterwards, every push to the tracked branch that touches `site/` or
`render.yaml` redeploys automatically, and a pull request touching those paths
gets its own preview URL.

## Custom domain

Add it under the service's **Settings → Custom Domains** and create the DNS
record Render shows you — a `CNAME` to the `onrender.com` hostname for a
subdomain, or Render's `A` record for an apex domain. TLS is issued
automatically once the record resolves.

Once the canonical domain exists, add it back to `index.html` as a
`<link rel="canonical">` and an `og:url` meta tag; both were left out rather
than guessed.

## Editing

Keep claims in step with the CLI. Every command shown on the page is one
Workbook implements today — check [`docs/reference.md`](../docs/reference.md)
before adding another, and do not put a proposed command on the page.
