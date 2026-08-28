# The baseline

What this file is: the promises whose failure means roster is **broken for a
normal user**, each pinned to the tests that hold it. Not everything the suite
proves — the suite proves far more — but the floor: the things a person doing
ordinary work with the ordinary documentation must never find out have stopped
being true.

It exists because of a day one of them did. The control plane refused every key
it held — the documented integration, whole — and nothing was red, because
every *part* was tested and the *journey* was not. The rule this file carries
is the one that day taught:

> **A change to code under a baseline promise runs that promise's tests before
> it ships, and a baseline test is never weakened to let a change pass.** If a
> promise itself has to change, that is a decision to write down first — beside
> the thing it decides — this table second, and the test third, in that order.

Every test named here is `cmd/…_test.go`, wire-level where the promise is
about a caller: it dials a served port and presents the credential a real
caller would. `TestBaselineNamesRealTests` reads this file and fails if a name
below stops existing, so a rename cannot quietly orphan a row.

The one journey test to know: **`TestTheTutorialRunsAsWritten`** is
[`usage/tutorial.md`](usage/tutorial.md) step for step — the CLI commands as
written, the wire calls over the transcoded data plane. If it disagrees with
the page, one of the two is wrong and both are load-bearing.

## First run, and every run after

| the promise | pinned by |
| --- | --- |
| `roster init` seeds one operator bound to `/roster.*/*`, prints the password once, and the identifier it prints is the row it wrote | `TestInitSeedsAnOperator` · `TestInitLeavesTheTwoFilesTheTutorialNames` |
| the password init printed signs the operator in; a wrong one, an unknown alias, and a data-plane holder do not | `TestAGivenPasswordIsTheOneThatSignsIn` · `TestAnOperatorSignsIn` |
| the shipped `roster.yaml`, unedited, is a working first run: `init`, two SQLite files, a server that builds and passes its readiness check | `TestTheShippedConfigurationIsAFirstRun` |
| a database that does not match the binary refuses to start, naming which plane — on **either** plane, with `migrate` off (the default) | `TestBothPlanesAreChecked` · `TestTheDataPlaneIsCheckedToo` · `TestServeRefusesAControlPlaneItWasNotAllowedToMigrate` |
| a restart changes nothing for anybody: keys, cookies, suspensions and revocations written before it hold after it | `TestARestartKeepsEveryCredential` · `TestTheCliMintsACustomersKey` · `TestAConsoleSessionSurvivesTheProcess` |
| `init` runs once; a second run stops before touching the operator | `TestInitRefusesToRunTwice` · `TestASecondSeedStopsBeforeTheOperator` |

## The tutorial's journey

| the promise | pinned by |
| --- | --- |
| the whole of `usage/tutorial.md`, as typed: four writes stand a customer up, `vouch reset` prints a working password, `key add` mints both kinds, curl verifies and reads over `server.http`, a narrow key is refused, `WhoseHost` resolves, revoke stops the key on the next call | `TestTheTutorialRunsAsWritten` |
| the same stand-up through the console's port instead of a shell, ending in a person who can be | `TestAnOperatorStandsUpACustomerThatCanBeUsed` · `TestAnOperatorAdministersCustomers` |
| creating a person is inert: no credential, no permission, nothing to present until a way in and a binding are each deliberately written | `TestAnOperatorStandsUpACustomerThatCanBeUsed` |

## Two customers, one deployment

| the promise | pinned by |
| --- | --- |
| an `rt_` reads its holder's tenant and no other — lists, gets, and **watch streams** all agree | `TestATenantKeyIsTheirsAndNotTheDeploymentS` |
| a slug naming another tenant names nobody, never a namesake in the caller's own | `TestATenantKeyIsTheirsAndNotTheDeploymentS` · `TestNamingSomebodyInAnotherTenantNamesNobody` |
| an alias is unique within a tenant only; the same alias in another tenant is another person, with another password | `TestAnAliasInAnotherTenantIsAnotherPerson` · `TestSomebodyNamedByTenantAndAlias` |
| an `rk_` is the deployment's and crosses tenants; an `rt_` can never be one, whatever prefix it wears | `TestATenantKeyIsTheirsAndNotTheDeploymentS` · `TestAKeyIsForOnePlaneOrTheOther` |
| an edge is not a way through the wall, and neither is anything the wall is waived for | `TestAnEdgeIsNotAWayThroughTheWall` · `TestNothingTheWallIsWaivedForCanNameAnybody` |

## Who may do what

| the promise | pinned by |
| --- | --- |
| deny by default: no binding, no call — whatever the credential | `TestSomebodyWithNoBindingMayCallNothing` |
| patterns match whole segments and are evaluated, never expanded: `/roster.HolderService/*` is one service, `/roster.*/*` survives an upgrade, a partial segment opens nothing | `TestARoleMayNameAServiceOrAPackage` |
| nobody grants what they do not hold, by any door — a role, a binding, a membership, a patch | `TestNobodyGrantsWhatTheyDoNotHold` · `TestNobodyWidensARoleTheyHold` · `TestNobodyAttachesARoleByPatchingAMembership` |
| nobody writes a way into an account wider than their own — a password, a factor, a key, an unlock | `TestNobodyWritesTheCredentialOfSomebodyWiderThanThey` · `TestNobodyMintsAKeyOnSomebodyElsesHolder` · `TestUnlockIsHeldToTheRuleResetIs` |

## Keys

| the promise | pinned by |
| --- | --- |
| `roster key add` mints once to stdout, `--allow` is required and a list, and the prefix follows from the flags — never from the caller | `TestTheCliMintsACustomersKey` · `TestAllowIsAListHoweverItIsWritten` |
| an `rt_` is mintable over the wire through its three doors, held to both escalation rules at each | `TestACustomerMintsTheirOwnKeyOverTheWire` · `TestNobodyMintsAKeyWiderThanThemselves` · `TestNobodyMintsAKeyOnSomebodyElsesAccount` · `TestAKeyIsNotMintedIntoAnotherTenant` |
| a person mints a key that acts as them and no more than them, from the wire and from their own terminal | `TestSomebodyMintsAKeyThatActsAsThem` · `TestSelfServiceOverTheWireWithHerOwnKey` · `TestTheCliIsAlsoACustomersPerson` |
| a key calls its allow list and nothing else, and never more than its holder | `TestAKeyReachesWhatItWasMadeFor` · `TestAKeyReachesNothingElse` · `TestATenantKeyIsTheirsAndNotTheDeploymentS` |
| a key is a row that exists — an identifier nobody minted is refused, on every plane, under every handler | `TestAKeyIsARowThatExists` · `TestAKeyNobodyMintedIsRefused` · `TestPlainDoesNotHandOutEveryTenant` |

## Signing somebody in

| the promise | pinned by |
| --- | --- |
| `Verify` with the right secret answers who; with anything else it answers **no and nothing more** — one indistinguishable `ok:false` for a wrong password, an unknown alias, an unknown tenant and an account with no ways in, through the served stack | `TestAWrongSecretIsRefusedAndSaysNothingElse` · `TestSomebodyWhoIsNotHereIsRefusedTheSameWay` · `TestEveryNoOverTheWireIsTheSameNo` |
| a delegation is bound to the person it was minted about **and** the key it was minted through; alone, or beside another app's key, it is worth nothing | `TestADelegationAloneIsWorthNothing` · `TestADelegationIsBoundToTheKeyAndNotToTheAppBehindIt` · `TestATenantKeysDelegationIsBoundToThePersonAndNotToTheKey` |
| enough wrong answers close the account before the password is compared, and getting it right clears what getting it wrong left | `TestEnoughWrongAnswersCloseTheAccount` · `TestGettingItRightClearsWhatGettingItWrongLeftBehind` |
| the console's cookie is minted by `POST /session`, ended by `DELETE /session`, and the end is immediate | `TestAnOperatorSignsIn` · `TestAConsoleReachesTheControlPlaneOverHttp` |

## The four ports

| the promise | pinned by |
| --- | --- |
| `server.addr` takes keys only — a cookie names nobody there, even though `/session` answers on its HTTP | `TestTheCookieOpensTheControlPlane` · `TestTheDataPlanesHttpSignsInNobody` |
| `control.addr` takes a cookie **and** a service's `rk_`; a customer's `rt_` has no meaning there | `TestTheControlPlaneAuthenticatesItsOwnKeys` · `TestACustomersKeyHasNoMeaningAtTheControlPort` |
| `admin.addr` is customers with no wall, behind an operator's session and nothing else — a key is not an operator | `TestAnOperatorAdministersCustomers` · `TestSigningOutReachesThePortWithNoWall` · `TestAnAdminPortWithNobodyToBeIsRefused` |
| every HTTP listener is the same server transcoded: the same interceptors, the same wall, a curl away | `TestTheTutorialRunsAsWritten` · `TestAConsoleReachesTheControlPlaneOverHttp` · `TestAConsoleReachesTheAdminPortOverHttp` |
| the CLI is a client: local mode is the box's, remote mode is walled and gated like anybody | `TestClientAddrChoosesTheWire` · `TestTheCliIsAlsoACustomersPerson` · `TestPlainReachesADeploymentWithNoControlPlane` |

## Stopping somebody, and it sticks

| the promise | pinned by |
| --- | --- |
| a revoke is a delete: the very next call bearing the key finds nothing, from either plane, and a revoked key's delegations die with it | `TestRevokingAKeyStopsItAtOnce` · `TestRevokingReachesTheKeyItNames` · `TestADelegationIsBoundToTheKeyAndNotToTheAppBehindIt` |
| `Disable` stops the person at every door on their next call — their keys, their delegations, their password, and a console session if they are an operator | `TestADisabledHolderIsNotToSignInAndNotSignedIn` · `TestASuspendedPersonIsSuspendedInFrontOfEveryApp` · `TestADisabledHolderIsRefusedBareOverTheWire` |
| `Invalidate` voids everything **issued** before now — sessions and delegations, the console's own included — and deliberately not named keys | `TestInvalidatingVoidsWhatWasIssuedBefore` · `TestInvalidateEndsTheConsolesOwnSessions` · `TestSomebodySignsThemselvesOutOfEverything` |
| an erase makes somebody unreachable at the wire — key, password, name — while destroying nothing | `TestAnErasedHolderIsStoppedAtTheWire` · `TestAnErasedHolderCannotAuthenticate` · `TestNothingOfAnErasedHolderIsReadableThroughARowThatOutlivedThem` |

## What an app in front is told

| the promise | pinned by |
| --- | --- |
| `Introspect` answers who a token stands for — the holder, not the key — and answers **no more than no** about everything else, real `rk_` included; and it is not public | `TestATokenSaysWhoItIs` · `TestATokenNarrowedToNothingStaysThatWay` · `TestIntrospectSaysNoMoreThanNo` · `TestIntrospectIsNotPublic` |
| the sync stream carries state, wall-narrowed by the credential alone: an `rk_` hears every tenant, an `rt_` hears its own, nobody is woken for a rename | `TestAnAppHearsThatADecisionStoppedBeingGood` · `TestTheSyncStreamAnswersToRealKeys` · `TestOneCustomerIsNotToldAboutAnother` · `TestNobodyIsWokenForARename` |
| `Me` answers the caller about themselves with no role needed for it, and no more than themselves | `TestMeAnswersAboutTheCaller` · `TestSomebodyWithNothingCanStillAskWhatTheyHave` · `TestMeAnswersWithNothingOfAnybodyElseS` |
| no verifier column leaves the building: not in an answer, not in the trail, not in the queue | `TestNoVerifierIsAnsweredByThePortThatServesItsRow` · `TestNoVerifierReachesTheTrailInEitherColumn` · `TestNoVerifierReachesTheQueueEither` |
