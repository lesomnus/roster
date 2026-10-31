# An app that keeps asking

[relying-party.md](relying-party.md) is how somebody's browser signs in to a
product, and [apps.md](apps.md) is which key the product holds beside roster.
This page is what comes after both. An app that has signed somebody in holds a
copy of a decision -- *this is that person, and they may do these things* --
and roster is where it stops being true: they are signed out everywhere,
suspended, erased, or the directory they are in is closed. So the app keeps
asking, for as long as it holds the copy.

That half is the same in every app, and it is `standing`, a package an app
imports -- for D22's reason, the one `frontdoor` was shipped for: written once,
by the repository that knows what the answers mean. It came out of Loft's
`internal/identity`, which wrote it first and runs on its rules, and it is what
levyan, keeping billing accounts that roster accounts own, uses rather than
writing it a second time.

## The three ways an app learns

| | | |
| --- | --- | --- |
| **asked** | `Accounts.Held`, over `Accounts.StandingOf` | on every call a session makes, what roster says now -- believed for `standing.HeldTtl` |
| **heard** | `Accounts.Follow`, or `Accounts.Follower` as a loop | roster's sync stream: what changed, as it changes, while the app is listening |
| **looked again** | `Accounts.Reconcile` and `Accounts.Settle` | every account asked about now and then, for what the stream missed; and an account asked about again before the app acts on what it heard |

Each covers what the one before cannot. Asking is the truth and costs a call, so
it is kept for half a minute. Hearing saves the asking and makes it prompt, and
it replays nothing. Looking again is the only one that sees what was written
while nobody listened, and what is never published at all.

`Accounts.Addresses` is beside them: an account's addresses that somebody
checked, which is what an invitation is found by.

## Wiring it

```go
accounts, err := standing.New(standing.Config{
	// roster's data plane, dialed as the app dials it -- or the roster the
	// app runs in its own process, over the bufconn it serves it on.
	Conn: conn,

	// A context whose calls roster answers as the app's holder in the
	// directory its accounts are in.
	As: as,
})
```

`Conn` is a `grpc.ClientConnInterface` and nothing more, so an external roster
and one in the app's process are the same calls. `As` is the app's because who
an app is at roster is its shape ([apps.md](apps.md)), and the package holds no
key:

| the app holds | `As` answers the context with |
| --- | --- |
| a deployment key (A, B) | `authorization: Bearer rk_…` and `roster-at: @<the directory>` |
| a tenant key (C) | `authorization: Bearer rt_…`, whose tenant is the directory |
| roster in its own process | payday's Plain, naming the app's holder: `@<the directory>/<its alias>` |

```go
// A deployment key: the tenant that nominated it, by `roster-at`.
as := func(ctx context.Context) (context.Context, error) {
	ctx = auth.BearerProvider(key).Provide(ctx)
	return metadata.AppendToOutgoingContext(ctx, front.HeaderAt, front.AtTenant("acme")), nil
}
```

An error from `As` is a roster that could not be asked, as far as `Held` goes,
and passes through as it is -- so an app whose `As` looks its directory up can
still tell *no directory yet* from *roster is down*.

### The role

`standing.Methods` is what the app's role at roster holds for all of this:
`HolderService/Get`, `SyncService/Watch`, `EmailService/List` and
`EmailService/Get`. It is the part of the role this package asks for; an app
adds what it asks itself -- Loft adds the password check, the tenant read and
its consent ledger.

```sh
roster app install --tenant acme \
  --role /roster.HolderService/Get,/roster.SyncService/Watch,/roster.EmailService/List,/roster.EmailService/Get \
  levyan
```

Installing again leaves a role the tenant has as the tenant has it, so an app
installed before it held the stream is widened by patching the role with the
whole list:

```sh
roster role patch @acme/levyan-app '{"methods": ["…"], "date_updated_force": true}'
```

Until then the stream is refused, and says so (below).

## Asked: what a session is held to

`Accounts.Held(ctx, account, began)` says whether something issued to an account
at `began` -- a session -- is still good. It answers `standing.ErrRefused` when
they were suspended, erased or signed out everywhere since, their directory was
closed, or roster has nobody by them, and `standing.ErrUnreachable` when roster
could not be asked. Both are refusals, the second because what roster would have
said is not known and a session is not served on a guess. The difference is what
the app tells its caller -- *you are signed out*, or *this is not your doing, try
again*.

- **What roster said is believed for `standing.HeldTtl`**, thirty seconds, and
  that it could not be asked for `standing.HeldRetry`, five: a roster that is
  down is not asked once per call. Nobody there is an answer, and kept as long
  as one.
- **A call that ends while roster is asked has asked nothing.** A page left
  half loaded, a call that ran out of its own time: the caller's doing, so
  nothing is kept of it, and the account's next call asks. Kept as roster's
  failing, it refused every call the account made for `HeldRetry`, and Loft
  found it as two browser tests failing for no reason they shared
  (`TestACallThatEndsWhileRosterIsAskedSaysNothingOfRoster`).
- **`Accounts.Recheck` drops what is known of an account**, for a change the
  app made itself to be seen at once -- an account it suspended at the roster in
  its own process, which has no stream to say so. A call that was already asking
  roster when the account was dropped keeps nothing of its answer, which may be
  from before the drop: the next call asks (`TestWhatIsDroppedWhileRosterIsAskedIsNotKept`).

`Accounts.StandingOf` is the asking with nothing kept. It reads the four facts
and nothing else -- no name, nothing declared personal -- because a read that
carries a person's data is a row of roster's access record, and a session held
to roster is asked about every thirty seconds; Loft's read of the whole row was
that row, every time (`TestAStandingIsNoReadOfSomebodysData`). And it reads the directory's
closing with them: asked from inside a closed directory roster answers nothing
at all, which is a roster that will not answer; asked from outside it, as a
deployment key on its own may be, the closing is the fourth fact
(`TestAClosedDirectoryIsTheFourthFactToAKeyOutsideIt`).

## Heard: the stream

`Accounts.Follow(ctx, on)` holds `SyncService/Watch` open and hands `on` each
account it speaks of, as a `standing.Standing` -- the four facts as they are
now, which is what the stream carries (`proto/app/sync.proto`). Each one is also
dropped from what this process knows, so the next `Held` asks: that is how a
suspension reaches a session already open at once rather than within
`HeldTtl` (`TestWhatRosterSaysArrivesAsItIsSaid`).

`Accounts.Follower(name, on)` is the same as a loop for payday's `spin.Run`,
named, and an app marks it to run in **every process**:

```go
s.Spin = append(s.Spin, spin.InEveryProcess(accounts.Follower("levyan.roster-stream", payers.Apply)))
```

Every process, because what the stream drops is that process's own knowledge.
Under `spin.lease` an unmarked loop runs in the one process that holds the
lease, and every other replica goes on believing what it was told for
`HeldTtl` -- a session that outlives a suspension in all but one of them, with
nothing to say so. The price is that `on` runs once per process for every
event, so what it does has to be safe to do twice, as writing down state is: a
session ended twice is ended (`TestTheFollowerIsALoopEveryProcessRuns`).

What it promises:

- **What changed while it was open, and nothing else.** Nothing is replayed, and
  opening it again forgets what was known of everybody rather than believing
  answers that may have changed unheard (`TestAStreamOpenedAgainForgetsWhatWasKnown`).
- **An erasure is heard even when it is the first thing heard of somebody**,
  which it usually is (roster#104,
  `TestAnErasureIsHeardOfSomebodyNothingElseHappenedTo`), and a directory closed
  is heard as an event about each person in it (roster#105,
  `TestADirectoryClosedShutsOutEveryoneInIt`).
- **A refusal is said once, as an error, and asked again rarely.** A role
  without `SyncService/Watch`, or a roster with no broker, is not something a
  retry fixes, so it is said in so many words -- `roster role patch`, or
  `watch.broker` -- and asked for again every `standing.RefusedRetry`, which is
  soon enough that the fix is noticed without a restart
  (`TestAStreamRosterRefusesIsSaidAndThenHeld`,
  `TestARosterThatPublishesNothingIsSaidOnce`). A stream that drops is a
  warning, and opened again within a second, then two, up to thirty.

The roster an app runs in its own process has no broker, so it has no stream,
and needs none: every change to its people is the app's own, which calls
`Recheck` after making it. An app does not run the follower for it.

## Looked again: what the stream cannot carry

roster's `restore` brings back somebody erased, while the grace lasts, and
`forget` destroys what was left of them. **Both write past roster's servers**,
and the stream is published from the servers -- so neither is ever heard. A
restore reaches the app as the next event about that person that is not an
erasure, whenever there is one (`TestARestoreIsNotHeardUntilSomethingElseIs`).

What an app does on hearing of an erasure is usually not done at once: Loft
marks each membership as leaving and waits out roster's grace before
pseudonymizing anything; levyan stops a payer at the end of the period. That
mark is a copy of roster's word, and the word may have been taken back. Hence the
rule:

> **A mark is acted on only after asking roster again.**

`Accounts.Settle(ctx, marked, gone, back)` is that one look: roster is asked
about each account the app marked, `gone` is called for the ones it still has
nobody by -- act on the mark -- and `back` for the ones it has again, restored
-- take the mark back. About an account roster cannot answer for, nothing is
decided, and it is asked again at the next look
(`TestAMarkIsActedOnOnlyOnceRosterIsAskedAgain`).

And what the stream missed because nobody was listening -- an erasure while the
app was down, or between a stream dropping and opening again -- is found by
asking about everybody. `Accounts.Reconcile(ctx, accounts, marked)` asks roster
about every account the app holds and answers the marks the stream should have
made (`Reconciled.Gone`) and the ones to take back (`Reconciled.Back`). It
decides nothing and writes nothing: the app puts its marks right, as it makes
them. Two refusals are built in, because each is a wrong configuration made to
look like news:

- **Everything is asked before anything is answered.** A roster that stops
  answering half way answers no marks at all, rather than the half it reached
  (`TestAReconcileThatCannotAskDecidesNothing`).
- **Nobody at all is not believed.** A roster that has none of the accounts,
  when some are unmarked, is far more often the wrong directory or a key that
  sees none of it than everybody having left -- so it answers what it found and
  a `standing.NobodyError`, which an app that marks does not mark on
  (`TestAReconcileThatFindsNobodyIsNotBelieved`).

An app runs both on a clock of its own, as the deployment's work -- one process,
under the lease, `spin.Called` and `spin.Every` -- and a reconcile on request
as well: Loft's operators have `identity reconcile`.

### Does the follower keep its place?

No, and there is no place to keep. A `SyncEvent` carries no sequence and roster
keeps no backlog to resume from, so a restart is a stream opened again, and is
met the same way: what was known is forgotten, and the reconcile pass finds what
was missed. A position would be a promise the stream does not make.

## Addresses

`Accounts.Addresses` is an account's addresses that somebody checked -- roster
holds them as verified, by a link it sent or by the provider the account signed
in through -- as roster stores them, folded. An address nobody checked is not
one of them, and that is the whole of the method: any directory's administrator
may put any address on a person of their own, and a provider passes it on, so an
invitation found by an unchecked address goes to whoever typed it
(`TestOnlyAnAddressSomebodyCheckedIsAnAccounts`).

## What stays the app's

- **What a change means.** Loft ends sessions and marks memberships
  `date_leaving`; levyan ends a payer's links when their period does. The
  package hands over `standing.Standing`, which is state, and the app decides
  what that state does to its rows.
- **Anything that writes at roster.** An app running roster in its own process
  makes its people, gives them passwords and suspends them through that
  roster's own server, and calls `Recheck` after.
- **Who it is at roster**, which `As` says, and the directory, which is the
  app's configuration.

## Where each fact is

| | |
| --- | --- |
| `standing/standing.go` | `Standing`, `Held` and its two lifetimes, `StandingOf`, `Addresses` |
| `standing/follow.go` | the stream, its refusals, and why it runs in every process |
| `standing/reconcile.go` | the mark, the rule, `Settle` and `Reconcile` |
| `proto/app/sync.proto` | what the stream promises, and why it carries state |
| `server/sync/sync.go` | how roster works out what changed, and an erasure heard on first sight |
| `server/forget/forget.go` | `forget` and `restore`, written past the servers |
| `login/sync.go` | the Login App's own follower, which carries a sign-out across to Hydra |
