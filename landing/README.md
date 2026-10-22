# landing

The public front door: one card that names the product and offers one link,
**Sign in with Google**, which opens the surface where Identity-Aware Proxy asks for the
account. Plain static files on Firebase Hosting, on a site of its own, so it needs no
sign-in and holds nothing from the product: no data, no script, no analytics and no request
beyond its own files.

| File | What it is |
|---|---|
| `index.html` | the page; the link's address is the surface's Cloud Run URL |
| `landing.css` | the sheet; its tokens are copied from `web/src/theme.css` |
| `fonts/`, `icon.svg`, `favicon.ico` | copies of what `web/public` ships |
| `../firebase.json`, `../.firebaserc` | the host's config: site `forge-wingman`, project `forge-wingman`, headers |

The design is `design/landing-v1.svg` in the vault. The button follows Google's own
branding rules for the light theme except for its font, which is IBM Plex Sans.

## Look at it

```sh
python3 -m http.server -d landing 4190   # then open http://127.0.0.1:4190
make landing-check                       # the copies still match web/, no stray files, no other address
```

Edit the copies at their source (`web/public`, `web/src/theme.css`, itself generated in the
vault) and copy them over; `make landing-check` fails until they agree. A font file that
changes keeps its name, and browsers keep it for a week (`firebase.json`).

## Cost

No charge for a site this size. Hosting's no-cost quota is 10 GB of storage and 10 GB of
transfer a month ([usage, quotas and pricing](https://firebase.google.com/docs/hosting/usage-quotas-pricing)).
The site is 58 KB in all and a first visit transfers about 52 KB, so the quota covers about
190,000 visits a month.

The project already has a Cloud Billing account (`gcloud billing projects describe forge-wingman`
reports billing enabled), and Firebase's docs say linking one upgrades a project to the Blaze plan
([pricing plans](https://firebase.google.com/docs/projects/billing/firebase-pricing-plans)).
So expect Blaze once the project is a Firebase project, with nothing to upgrade, and the no-cost
quota still applies; the Firebase console's usage page shows the plan after the deploy. The
difference from Spark is what happens past the quota: the site keeps serving and bills
$0.15 per GB of transfer and $0.026 per GB of storage, where Spark would disable it until the
next month. Past 10 GB that is 100 GB of extra transfer for $15. A budget alert on the billing
account is the guard if that matters.

## Deploy (owner steps)

Run from the repo root. Nothing here has been run; `projects:addfirebase` and `deploy` are the
two that change the project.

```sh
fb() { npx --yes firebase-tools@15.34.1 "$@"; }   # needs Node 20 or later; the version is pinned here

fb login                                    # a browser opens; choose alvin4292@gmail.com
fb projects:addfirebase forge-wingman       # makes the existing Google Cloud project a Firebase project
fb hosting:sites:list --project forge-wingman
```

`projects:addfirebase` enables the Firebase APIs on the project. If it stops on the Firebase
terms, open <https://console.firebase.google.com>, choose **Add project**, pick the existing
`forge-wingman`, accept, and run the commands from `hosting:sites:list` on.

The list should show the default site `forge-wingman`. If it is empty, create it:

```sh
fb hosting:sites:create forge-wingman --project forge-wingman
```

Then publish:

```sh
fb deploy --only hosting --project forge-wingman
```

The public address is **<https://forge-wingman.web.app>** (also
`https://forge-wingman.firebaseapp.com`). Check it from a machine that is not signed in:

```sh
curl -sI https://forge-wingman.web.app/ | grep -iE '^(HTTP|content-security-policy|cache-control)'
```

It should answer `200`, not a redirect to `accounts.google.com`, and carry the policy from
`firebase.json`. To take it down: `fb hosting:disable --project forge-wingman`.

If the surface's address changes, edit the one `href` in `index.html` and deploy again.
