# Working on roster

The rules are in [CLAUDE.md](../CLAUDE.md) -- what is generated, what is yours,
what to run before pushing. This page is the part that is background rather than
rule: why the packages are arranged as they are, what the generation loop is, and
what the generator refuses.

## Where to start reading

| | |
| --- | --- |
| `cmd/serve.go` | the stack written out -- which layers, in which order, and which server the wall is on. Deliberately not hidden behind a `payday.Serve(cfg)` |
| `proto/app/identity.proto` | an entity. The `(payday.entity)` option at the bottom is where the domain byte, the tenant wall, the `List` and the `Watch` come from |
| `cli/cli.go` | why the commands and the database drivers are a package of their own, in megabytes |
| `server/core/` | every rule no schema can state, each in a file named after it |

## Two names, and the reason for each

The generated messages are Go package `rstr` and proto package `roster`, which is
why the binary lives in `cmd/roster/` rather than beside them.

**`rstr` and not `api`**, because these messages are meant to be imported by
other apps, and `api` is what every payday app calls its own generated package --
a product importing roster's would be aliasing one of the two in every file that
mentions both.

**proto package `roster` and not `app`**, because protobuf's file registry and
payday's `pdid` domains are per **process**: two payday apps link into one binary
only if their proto packages differ. The reference app does, and a product
embedding roster's sign-in flow will.

To put the messages somewhere else, change `option go_package` in
`proto/app/*.proto` and regenerate. Every entity has to say the same one -- an
app is one Go package for everything generated, because the ent schemas of two
packages cannot have an edge between them and the tenant wall is an edge.
`pd gen` refuses the alternative rather than generating an app whose wall has
nothing to stand on.

## Generating

```sh
go tool pd gen .          # messages, servers, ent schema, layers
go tool pd gen --ts .     # and the TypeScript half
go tool pd doctor .       # what would go wrong before it does
```

`pd gen --check --ts .` is what CI runs: a generated file that was not
regenerated **compiles perfectly and is wrong**.

`pd gen` also pins the buf dependencies the first time, which is not a
convenience. `buf dep update` compiles the workspace before it writes the lock,
and this app's schema names `Tenant` -- which does not exist until a generation
has copied payday's entities into `proto/roster/payday/`. So there is exactly one
moment it can run: inside `pd gen`, between those two things.

## Upgrading payday

```sh
GOPROXY=direct go get github.com/lesomnus/payday@<sha>
go tool pd gen . && go tool pd gen --ts .
```

By commit and not `@main`: the module proxy caches what `@main` resolves to, so a
`go get` right after a push reports success and changes nothing.

payday owns some of this app's schema -- `Tenant`, `Holder`, `Audit`, `Outbox`,
and the four the record is kept with (`Access`, `Archived`, `Owed`, `RowSeal`) --
so a field added to one of them there arrives in `internal/ent` here the next
time you generate, and **nothing about that is loud on its own**. It compiles,
the tests pass against a database the tests just created, and the first sign of
trouble is a column that is not there in the one handler that reads it.

Two things refuse rather than trusting anybody to remember:

- **a `pd gen` that did not happen.** `server/pd/pd.g.go` carries the payday it
  came out of, and `pd.NewSink` refuses a binary linking a different one.
- **a migration that did not happen.** `serve` looks at the database before it
  answers anything and refuses one that is not the shape `internal/ent`
  describes, printing the SQL that is missing.

Neither says anything when it cannot -- a `replace`, a workspace, a build with no
version are all somebody developing. While developing, `db.migrate: true` swaps
the second refusal for `ent.Schema.Create`; that is a decision about who may
alter tables, so it should be made on purpose rather than left on. There is no
migrations directory. From the first app's launch on, what a change means for a
running deployment is written beside the change; until then, a change that breaks
what one stored moves the compatibility epoch instead and says which roster to
migrate on first ([CLAUDE.md](../CLAUDE.md)).

## Adding an entity

```sh
go tool pd entity add --tenanted --watch Widget .
go tool pd entity list .
```

Rather than writing the file: it picks a domain nothing else has and writes the
tenancy out, which are the two things that are cheap to get wrong and expensive
to find later. CLAUDE.md has the field-number convention that is read by name
across every entity.

## The pages

```sh
cd ts && npm install
npm run dev              # the admin console, against a running roster
npm run dev:sandbox      # the admin console with the whole server compiled into the page
npm run dev:user         # the user console, against a running roster
npm run dev:user:sandbox # the user console with the same server compiled into the page
npm run dev:account:sandbox # the account UI with sample APIs in browser memory
npm run dev:login        # the sign-in pages, with a made-up server behind them
```

`ts/` builds four pages over one `ts/lib/` and one `ts/gen/`: the **admin
console** (`ts/console/`), the **user console** (`ts/user/`), the **account
page** (`ts/account/`) and the **Login App's** (`ts/login/`). The two consoles
draw one tenant's rows with the same components -- `ts/lib/tenant/`, because
what differs between them is who is calling and which listener answers, and
neither of those is something a table of holders knows about. Two pages draw a
sign-in, so it is one component --
`ts/lib/signin.tsx`, the password form, the second factor and the operator's
provider buttons. What is shared is what `frontdoor/web/frontdoor.js` says is
worth sharing; the markup around it is each page's own.

The shell around those screens is one too (`ts/lib/console.tsx`), and its sidebar
has a **head**: the tenant, drawn large, naming every screen under it and opening
the tenant's own. That is a `Tab` like the rest -- it has an address, it can be
refused, and only its drawing differs -- so `Tab.lead` is the whole of what says
so. Above the head the two pages differ and are meant to: the admin console has
the picker that chose the tenant, and the user console has nothing, because it is
reached at the tenant's own host and the tenant is the whole of what it is.

What is worth reading is what is **not** in them: nothing declares which query a
write invalidates, nothing pushes a new row into a list, and nothing joins up the
tenant shown beside a row with the one at the top of the page. That falls out of
the reads going through the framework -- `useQuery` makes the call, so the store
knows which rows were drawn, and everything drawing a changed row re-renders at
once. `ts/lib/store.ts` is the whole of the local store, including the IndexedDB
mirror that makes a reload draw the page it had; leaving out one field turns the
mirror off.

**A store is its caller's.** The server names one for the session the browser
holds -- `AuthService.Which` on a console's listener, `GET /session` at the
account app -- every call the store's queries make says that name
(`payday-identity`), and a call is served only as the session it names, at the
session handler or at `frontdoor.Door.Proxy`. The cookie is every tab's, so
whoever signed in last in any tab is who a page's next call is made as; a page
still holding the store of the person before is refused rather than reading this
person's rows into it. So a page opens its store once somebody is signed in, not
for its lifetime: it asks again who is signed in when a call is refused and lets
go of the store if that is somebody else now, forgets it on a sign-out in any tab,
and deletes every other caller's mirror once its own is open (`ts/lib/caller.ts`).
A sign-out whose forget could not reach this browser's copy is said on the form
it ends at (`stillHere`), since only the person can take it off the browser
then.

`pd gen --ts .` writes `ts/gen/`, including `entities.ts` -- one declaration per
entity, which is what the store is built from. Nothing there is behaviour, and
nothing is generated per service: `ts/lib/client.ts` turns a descriptor into a
client in one line.

**Nothing imports upward.** A specifier starting with `./` names a file in the
same directory; anything crossing one says `#lib/...` or `#gen/...`. Those are
Node subpath imports, declared in `ts/package.json` and nowhere else -- `tsc`,
`vite build` and the dev server each read that one field, so there is no alias in
any of the four vite configs to be kept in step with it. And `#` cannot be the
start of an npm package name, so nobody has to work out whether a path is a
dependency.
`ts/imports.mjs` refuses a `../`, and `npm run check` runs it before either
`tsc`. It reads the four pages, `ts/lib/` and `ts/e2e/`; the shape of `ts/gen/`
is the generator's to decide.

React is a **peer** dependency of payday and an optional one. `payday/store` and
`payday/query` know nothing about it; `payday/react` is thirty lines of
`useSyncExternalStore` over them.

### The sandbox

The account page uses `@lesomnus/nox` for Section surfaces, forms, tables and
buttons. Layout lives in `ts/account/style.css`; it does not load
the consoles' global control styles. Shared sign-in and picture behavior accepts
the shared Nox controls used by the other pages as well.
Settings start directly with the profile section, without introductory copy.
Each panel is a Nox `Section` within `SectionLayout`, using its native surface,
padding and content gaps rather than the Card wrapper and shadow.
Settings share a 600px maximum width with the account toolbar. It contains only
the company mark and a labelled sign-out icon; appearance follows the system.
Its padding is 8px and outer radius is 20px. Including the 1px border gives a 9px
button inset, so the button's 11px radius has the same corner centres. The Nox
default button stays transparent until hover. The toolbar floats
16px below the viewport edge as a rounded translucent island, with a blurred
backdrop over the full-width badge scene. Its sticky track ends with the opening,
so the rising settings push the toolbar out rather than leaving it over the forms.
Both use 16px side gutters on narrow
screens. The opening scene has no corner headings, footer copy or action button;
scrolling continues to the settings below it over a fixed scene background.
Nox is not published to npm yet, so `ts/vendor/README.md` records the upstream
commit and the unmodified `npm pack` archive installed by `npm ci`. The Docker
page stage copies that archive before installing dependencies too.

The account opens with `ts/account/badge.tsx`. Its plate, clasp and
necklace are Three.js/WebGL meshes built by `ts/account/badge-model.ts`: a
bevelled extruded plate with a real slot, a flat metal crimp over two overlapping
strap ends, and a
long ribbon with two broad faces and thin edges. The ribbon has a rectangular
cross-section, a woven bump texture, and the existing company mark and name
repeated along both faces. It deforms and twists in three dimensions; it is not
a tube or a stroked SVG path. `ts/lib/badge-ribbon.ts` simulates its centreline
with pinned ends, fixed link lengths, uniform flexural stiffness, air drag and
weak buoyancy acting on the centre of mass. A seeded random stream gives each
mount different, smoothly changing gusts. The strip is 21.30 world units long,
twice the accepted initial length, with the same simulation and mesh resolution.
A heading lasts 14–24 seconds and
transitions with a 4-second response, giving the longer strip time to travel before
turning. Long-span bending stiffness and a single broad torsional mode suppress
small wrinkles without pinning creases at fixed material indices.
A weak, damped torsional spring gradually favours a broad face over an edge-on
fold. It treats both faces equally, so it relaxes lingering twists without
snapping sections over or holding the ribbon rigidly flat. Weaker centre-of-mass
confinement permits a wider drifting range while depth contact is retained.
There are no per-particle rest positions, remembered creases or fixed twist
patterns. A rounded starting curve avoids the old initial cusp. Minimum-twist
cross-section transport, temporal director smoothing and damped torsional motion let
the folds move along the material. There is no scripted fall, upward kick or
repeating wave cycle. Its material
coordinates are fixed too, so the printed marks do not stretch or slide.
This constrains a ribbon centreline rather than simulating a full cloth surface.

The scene builds its lighting environment locally and fetches no model or HDR
assets. The background and WebGL canvas stay fixed in the viewport. Scrolling
raises the badge within that view in world space; only its clamp pulls the free
ribbon, which retains its inertia. The scroll lift is eased and speed-limited
on the same clock as the physics, and reverses on scroll-back. After two viewport
heights the entire loop is above the view, so drawing pauses until scroll-back.
The settings scroll in front of the fixed scene in a separate document layer.
The card itself rises
from below the viewport over 2.4 seconds, using a monotone curve with zero speed
and acceleration at both ends. The initial loose loop hangs below the card;
moving clamp points pull it upward through the physics. A damped momentum pickup
during ascent lets the free ribbon coast upward after the card stops, with no
impulse at the stop and no repeated rise of the card. The motion and simulation
share a bounded clock so slow frames do not make the mount jump ahead of the
cloth. Reduced motion starts in the final pose; the HTML fallback keeps its CSS
entrance. The card gently
rocks forward/back around the portrait. Raycast hover settles the
badge exactly front-facing and still, while the cloth keeps moving; stable hit
bounds prevent hover flicker. Animation pauses outside the viewport and in
background tabs, reduced motion is still, and GPU draws are capped at 30fps
with a rounding tolerance so a 60Hz refresh does not accidentally drop to 20fps.
The default pose is front-facing; pointer coordinates use the viewport centre
with no constant yaw/pitch/roll offset. The card rotates around its portrait,
whose centre is placed at the viewport centre, accounting for the preview bar above
the canvas. Pointer translation follows a radial cubic ease-out: with
`s = min(distance / (0.45 * min(viewportWidth, viewportHeight)), 1)`, its radius
is `0.48 * (1 - (1 - s)^3)` world units. The incremental response falls to zero
at the cutoff and stays capped beyond it. Translation is eased over time,
settles back to centre on hover and is still for reduced motion. Tracking uses
the entire viewport, including the toolbar above the hero. Only the attachment
points move with the card; the free ribbon retains its world-space inertia.
The card is scaled to 84% to give the moving fold more room.
The original physical materials (plastic clearcoat, fabric sheen) and
up-to-1.5x pixel density are retained; there is no total pixel cap. There is no
floor or floor shadow. The crimp flexes slightly with the ribbon's drift, and
both overlapping pinned ends and their short clamped exit tangents follow its
rear plates. There is no hook or split
ring. The ribbon's particles,
velocity history and mesh remain in world space. Only the two attachment points
follow the clasp, interpolated across 120Hz simulation steps, so mouse-driven
turns pull the ribbon through its constraints rather than rotating it as a child
of the badge. Contact over the card's footprint keeps the whole cross-section
behind the card. Free sections are not swept along by an infinite rotating plane.
Adaptive forward/backward length projection handles abrupt attachment changes
without letting transient stretch persist.
A short return section inside the crimp joins the strip into a closed
band. Its fixed length counts toward the same total material length; the mesh is
watertight across both this connection and the texture seam. Position constraints
and velocity constraints are solved separately so length/bend/contact corrections
do not inject artificial spin. Bulk angular drag preserves local bending and
translation inertia; a weak depth envelope keeps the floating band from rolling
edge-on. Idle geometry tests measure the actual band plane for fast rotation and
flips, alongside the length, attachment and closed-surface checks.
The 96-link physical centreline drives a separate display surface: 576 free
curve intervals and 48 intervals through the held return. Smooth fairing blends
in gradually after the clamped necks, then a closed centripetal spline is sampled
by arc length. Length corrections preserve both ends and their endpoint slopes,
so smoothing neither stretches the strip nor breaks its joins. Physical bend
constraints prevent one-link corners; a coherent parallel-transport frame with
unwrapped roll replaces independent per-section orientation choices. This keeps
the broad faces from flipping or pinching on tight turns. The compressed ends
blend smoothly into the full strap width. Geometry tests check both triangles
of each broad-face quad and adjacent surface directions under rapid clasp turns.
The renderer disposes its owned
GPU resources on unmount. The settings and other pages do not load the Three.js
chunk until the scene mounts.

The front is real HTML in `ts/account/badge-front.tsx`, backed by the settings
editor's same holder query. A 1024×1448 CSS surface is projected through the
badge and camera matrices, preserving perspective while it rotates and scrolls.
The physical front plate stays in WebGL; the HTML leaves the slot and crimp
unpainted. The face has a round portrait slightly above centre, the existing name
and alias treatment, a five-digit Nox `CompactNumber` at the lower left, and only
the company mark at the lower right. It has no department or other printed copy.
Prefixed employee IDs retain their stored value; their numeric part is displayed
in five slots (for example `E-1042` becomes `01042`).
Clicking the name or employee number edits it in place with only a background
change, retaining the text's type and position and the alias. There are no field
borders, underlines, hints or action buttons. Enter or blur saves, Escape cancels,
and IME composition does not submit. An eager completion guard prevents a blur
from repeating an Enter save or committing a cancelled draft. Success is announced
only to assistive technology; failures remain visible. Focus and active editing hold the card
front-facing even when the pointer leaves. A one-field save clones the current
profile and retains its other values, with `dateUpdated` for optimistic locking.
Settings inputs track fresh server values until edited locally; unsaved local
fields survive card writes rather than being reset by the shared query update.
The photo area uploads the original file through `Holder.Portray`, using the
same 2 MiB limit as the settings picker; it also offers removal. Native HTML
images can display remote URLs without drawing them into a canvas texture.
Write controls follow the corresponding Update/Portray permissions, and edits
to a directory-managed profile retain the ownership rule in the input's accessible
description and the settings' visible notice.
Offscreen card controls are inert, so tabbing through settings cannot focus a
hidden card. The same face becomes a static usable card if WebGL is unavailable
or its context is lost. The account settings follow the scene in normal document
order.

For account UI work, `npm run dev:account:sandbox` opens the real page at
`http://localhost:5174` with its API responses made up **inside the browser**
(`ts/account/sandbox.tsx`). No roster process, Docker, Wasm build or API server
is needed. The preview bar switches between an account, the sign-in, an empty
account, a second-factor sign-in, a directory-managed account and limited
permissions. It starts signed in as `erin`; the sign-in fixture uses
`erin` / `correct horse battery staple`, and the MFA fixture uses `frank` with
the same password and code `123456`. The preview bar says which to use.

Profile, addresses, passwords, factors, sessions and keys can be changed in
memory. Resetting or reloading restores the fixture, and no store mirror is
written to IndexedDB. Provider round trips and security keys are simulated;
mail endpoints show the sent state without sending mail or delivering a link.
The terminal preview recognises `DEMO-CODE`. This is for drawing and interacting
with the UI, not checking authentication or authorization. Only a development
build with `VITE_ACCOUNT_SANDBOX=1` imports it; ordinary `dev:account` still
uses the real account backend.

`npm run test:account:sandbox` drives this preview with Playwright and no backend.
`./scripts/e2e.sh` includes it alongside the four pages against their own servers.

`npm run dev:sandbox` compiles the server into the page -- `GOOS=js GOARCH=wasm`,
SQLite in a Worker, a message port instead of HTTP/2. A reload is a fresh
deployment. One instance serves **two** servers, because the admin console reaches
`control.http` and `admin.http`, and the page dials the second by name on the
same socket. The cookie cannot work over a message port, so `wasm/sandbox`
remembers who signed in -- a sandbox being a sandbox, and `wasm/main.go` says how
far that goes.

It does not migrate: `wasm/schema` is the same tables as one SQL script, kept
true by `TestTheScriptIsThisSchema`.

## A browser cannot speak gRPC

Which is why every listener can have an `http` block beside it:

```yaml
server:
  http:
    addr: ":8080"
    allow_web: true
    origins: ["http://localhost:5173"]
```

What answers there is the **same** server -- the same interceptors, the same
credential, the same wall -- speaking Connect and gRPC-Web. A Connect call is a
POST with a JSON body, so it is also what to reach for from a shell:

```sh
curl -sX POST http://localhost:8080/roster.HolderService/List \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer ${KEY}" -d '{}'
```

Under TLS the same listener carries native gRPC as well, by ALPN.

**The sign-in is not a route.** It was `POST /session` on every listener that had
HTTP, on the reasoning that issuing a credential is HTTP because `auth` reads one
and never makes one. Half true: a cookie is a response header, and `web.Transcode`
hands `set-cookie` metadata to the browser as one -- so the admin console's sign-in is
`AuthService.SignIn`, an RPC like every other call the page makes. It also
removed what the route cost, which was the data plane's port answering an
operator's password with a cookie that opened nothing.

## What the generator refuses

Every one of these fails **quietly** when it is left out, which is why none of
them is a convention:

| | what it costs to forget |
| --- | --- |
| `domain:` | identifiers that say nothing about what they name |
| a domain twice | an identifier that lies about what it names |
| tenancy unsaid | every row outside the wall, with nothing failing |
| `watch:` with no version | a stale answer overwriting a fresh one, on the client |
| `watch:` with no `ref` filter | a stream that cannot say which rows it is about |
| an overlay on payday's own field number | `alias` quietly becoming whatever the overlay said |
| a list order not ending in the key | a page that repeats a row or skips one |

And one warning rather than a refusal, because a small table is a real thing: a
list order no index covers. An alias that is not a name is refused at generation
and again at run time.

## See also

- [CLAUDE.md](../CLAUDE.md) -- the rules, and the gates to run before pushing
- [docs/baseline.md](baseline.md) -- the promises, each pinned to its tests
- [docs/roadmap.md](roadmap.md) -- what was built, in order, and what it cost
- payday's [guides](https://github.com/lesomnus/payday/tree/main/docs) -- the
  framework this is written against

### Shared Nox presentation

All four pages — Admin Console (`ts/console`), User Console (`ts/user`), Account
(`ts/account`) and Login App (`ts/login`) — use the pinned Nox package documented
in `ts/vendor/README.md`. `ts/lib/presentation.tsx` provides the theme, labelled
native controls and sign-in adapters. Each app retains its own transport and
authorisation flow. Admin and User Console share the navigation and workspace
in `ts/lib/console.tsx`, and their tenant screens remain shared in `ts/lib/tenant`.

`ts/lib/style.css` arranges the consoles and Login App using Nox theme tokens;
Account keeps its badge and page layout in `ts/account/style.css`. Tables keep
their native semantics and scroll inside narrow screens. Add/edit forms use a
Nox drawer with focus containment, Escape dismissal and focus restoration.
