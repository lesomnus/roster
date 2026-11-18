# What roster decides about a call

What this page is: **every rule a call is held to, one line each** -- where it
is written, and the test that holds it. [permissions.md](permissions.md) tells
the same thing as a story, with examples; this is the list to check a
deployment, a page or a change against.

Five kinds of thing are on it, and what tells them apart is what can change
them:

| | what it is | what changes it |
| --- | --- | --- |
| **Rules that hold whoever is asking** | written into `server/core` and the listeners | nothing a tenant or a deployment configures |
| **Where a grant reaches** | five places a role is held in, and what each reaches | which role is bound where; never how far a place reaches |
| **What roster does by itself** | rows it writes that nobody asked for by name: who administers a new tenant, who manages a new group | nothing; they are undone by the ordinary writes |
| **What a tenant or a deployment switches** | a setting, or the list of one of roster's own roles | whoever may write it |
| **What roster leaves** | things it could have had a rule about and does not | a deployment, by refusing ([#305](https://github.com/lesomnus/roster/issues/305), not built) |

The last section reads the list back: which of the first kind are roster's
opinion more than anybody's safety, and so might be a deployment's to say.

`TestTheRulesPageNamesRealTests` reads this file, and fails for a test named
here that is not there.

## In what order a call is decided

1. **Who is calling**, by the credential -- and the credential's own list of
   methods, which only ever narrows what its holder may do.
2. **The gate**: a role of theirs names the method. A few methods ask for no
   role: they take no *whose* (`MeService`), or are their tenant's
   `self-service` role, which reaches nobody else.
3. **The wall**: which rows the call reads and writes. Another tenant's are
   never among them, and inside a tenant it is where the caller holds *the
   method this call is*.
4. **The rules**, about this request and this row.

A refusal at 3 is `NotFound`, since a row somebody may not see is not said to
be there. At 2 and 4 it is `PermissionDenied`, or `FailedPrecondition` where
what is refused is the state the write would leave.

## Where a grant reaches

A role is a list of methods. Holding one somewhere is a **grant**, and it
reaches where it was made -- for each method on its own, whatever else its
holder holds elsewhere.

| held | by | reaches | held to it by |
| --- | --- | --- | --- |
| **across the tenant** | a binding with no site, to somebody or to a group they are in; a grant a file makes ([file-policy.md](file-policy.md)) | every row of the tenant | `TestARoleIsWhatOpensIt` · `TestAGroupCarriesItToo` |
| **in a site** | a binding made in that site | what is the site's -- its roles, bindings, teams, groups, and each one's memberships -- and **the people who are of it** (`SiteMembership`), with what is theirs | `TestABindingInASiteNarrowsTheSecondAxis` · `TestAGrantMadeInASiteReachesThePeopleOfThatSite` · `TestAPlaceInATeamIsReadWhereTheTeamIs` |
| **in a team** | a role held in it (`TeamMembership.role`) | the team's site, as a binding there does; and of the team's members, **that team's** alone. A team of no site reaches the tenant | `TestATeamAdministratorManagesTheirOwnTeam` · `TestARoleIsPerTeamAndThereforePerSite` |
| **in a group, managing it** | a manager's role held in it (`GroupMembership.role`), which names only what changes a group | that group: who is in it, what it is called, erasing it | `TestAGroupsManagersDecideItsMembers` · `TestARoleHeldInAGroupReachesNoFurther` · `TestAManagerOfAGroupReachesWhoTheyPutInIt` |
| **nowhere** | the tenant's `self-service` role, which everybody of it holds and nobody is bound | **the caller's own** rows, and nobody else's | `TestWhatSomebodyDoesToTheirOwnAccountNeedsNoRole` · `TestWhatIsSomebodysOwnReachesNobodyElse` |

And of all five:

| | held to it by |
| --- | --- |
| a call is narrowed to where its caller holds the method it is: a role held across the tenant does not carry one held in a site anywhere else | `TestAGrantReachesWhereItWasMadeWhateverElseIsHeld` |
| somebody's own rows are theirs to read and write wherever they are, or nowhere | `TestAPlaceInATeamIsReadWhereTheTeamIs` · `TestWhatIsSomebodysOwnReachesNobodyElse` |
| somebody of no site is reached by whoever holds the method across the tenant, and by themselves | `TestAGrantReachesWhereItWasMadeWhateverElseIsHeld` |
| a row a read is asked to bring along (`select`) is read by its own rule, and one out of reach comes back as its reference alone | `TestASelectReadsAnEdgeByItsOwnRule` |
| being on a team with no role, or in a group nothing is bound to, gives nothing; being of a site gives the person nothing, and puts them within a grant made there | `TestAMembershipWithNoRoleIsStillAMembership` |
| no grant reaches another tenant | `TestARoleDoesNotCrossTheWall` · `TestTheTenantWallStillApplies` |
| where somebody reaches is said once a site, in the order the sites were made, to them (`MeService/Get`) and of them (`HolderService/Reaches`) | `TestWhereSomebodyReachesIsSaidOnceAndInOrder` |

There is no sixth place. What wants something finer -- *the people of this
group, and of that one not* -- is what roster leaves.

### What a grant made in part of the tenant never writes

These are the tenant's as a whole. A method held in a site, in a team or in a
group does not write one, whatever it names (`server/core/tenantwide.go`).

| | held to it by |
| --- | --- |
| the tenant itself: `TenantService`'s `Close`, `Restore`, `Update` | `TestAVerbAboutTheWholeTenantIsHeldAcrossIt` |
| `Connection`, `Host`, `HostProof`, `MailDomain`, `Nomination`, `Site`: every write of each | `TestWhatIsTheTenantsAsAWholeIsWrittenAcrossIt` |
| a person made, and a person erased who is not the caller; who is of which site | `TestAGrantMadeInASiteReachesThePeopleOfThatSite` |
| a team or a group of no site, made | `TestAPlaceInATeamIsReadWhereTheTeamIs` |
| and a team of no site is named once in its tenant, as a team of a site is in its site, under two callers at once too | `TestATeamOfNoSiteIsNamedOnceInItsTenant` |
| which relying party's tokens carry a group (`GroupExposure`) | `TestWhoAGroupIsShownToIsTheTenantsToSay` |
| the trail, read | `TestTheTrailIsTheTenantsAsAWhole` |

## The rules that hold whoever is asking

### Nobody is given more than they were given

| | written in | held to it by |
| --- | --- | --- |
| No binding, no call about anybody else | `cmd/policy.go` | `TestSomebodyWithNoBindingMayCallNothing` |
| A pattern in a role matches whole segments and nothing smaller | `cmd/policy.go` | `TestARoleMayNameAServiceOrAPackage` |
| A credential narrows and never widens: a key calls what its list names **of** what its holder holds, a delegation likewise, and an empty list is not everything | `cmd/auth.go` | `TestAKeyReachesWhatItWasMadeFor` · `TestAKeyReachesNothingElse` · `TestAnEmptyGrantColumnIsNotAWholeGrant` · `TestATokenNarrowedToNothingStaysThatWay` |
| A delegation is the person it is about and is worth nothing without the key it was given to | `server/keys/delegate.go` | `TestADelegationIsThePersonAndNotTheApp` · `TestADelegationAloneIsWorthNothing` · `TestADelegationIsBoundToWhoeverWasGivenIt` |
| An app is handed a delegation for the methods it may itself call -- by its credential's list **and** by what whoever it is has been bound -- and for no other | `server/vouch/delegate.go` | `TestADelegationIsWithinWhatItsCallerMayCall` |
| A request says who is asking one way: a header that is there and names nobody is refused, two keys are neither, and a header answers before a cookie does | `server/keys/acting.go` | `TestActingForNobodyIsNotCallingAsOneself` · `TestWhatSaysWhenSomebodySignedInIsWhatTheCallCameWith` |
| A key of the deployment's, narrowed to a tenant, is whoever that tenant nominated for it, and nobody until a request names the tenant | `cmd/auth.go` | `TestADeploymentKeyIsNarrowedToWhoATenantNominatedForIt` · `TestANarrowedKeyDoesNothingUntilARequestNamesATenant` · `TestAKeyNobodyNominatedBorrowsNothing` |
| A key of the deployment's is not the deployment: it calls its list, and is held to every rule here | `server/core/declared.go` | `TestAKeyOfTheDeploymentsIsNotTheDeployment` |
| What somebody is told they hold is what the gate enforces | `cmd/policy.go` | `TestMeSaysWhatTheGateSays` · `TestMeAnswersWhatTheCredentialLeavesOfWhatIsHeld` |

### Nobody hands out what they do not hold

Each is asked at the scope the thing is handed out at, and one of the giver's
grants has to cover the method **on its own** (`server/core/escalate.go`,
`mayGrant`).

| | held to it by |
| --- | --- |
| A binding: the role's methods, where the binding is made | `TestNobodyGrantsWhatTheyDoNotHold` · `TestNobodyGrantsEverythingWhoDoesNotHoldIt` · `TestNobodyGrantsAPatternWiderThanAnythingTheyHold` |
| A role written or widened: its whole list, where it may be bound | `TestNobodyWidensARoleBeyondWhatTheyHold` · `TestAGeneralWriteIsNotAWayRoundTheEscalationRule` · `TestATransactionIsNoWayRoundTheEscalationRule` |
| What is held in a site is handed out in that site, and not across the tenant | `TestASiteAdministratorStaysInTheirSite` · `TestAGroupBoundInOneSiteIsNotAGroupBoundAcrossTheTenant` |
| A role held in a team: its methods, at the team's site | `TestAttachingARoleIsGrantingIt` · `TestNobodyAttachesARoleByPatchingAMembership` · `TestATeamRoleIsNotYoursToHandOut` |
| Putting somebody into a group: everything bound to the group | `TestJoiningAGroupIsGrantingYourselfWhatItHolds` · `TestAGroupBoundToARoleStillAsksWhoeverJoinsSomebody` · `TestWhatYouHoldYouMayPutSomebodyInto` |
| A manager's role names only what changes a group, and a manager does not appoint another | `TestARoleHeldInAGroupReachesNoFurther` · `TestAGroupsManagersDecideItsMembers` |
| A key's methods | `TestNobodyMintsAKeyForWhatTheyDoNotHold` · `TestNobodyMintsAKeyWiderThanThemselves` · `TestNobodyMintsThemselvesAKeyWiderThanTheyAre` |
| An app's key answered as somebody: that person holds no more than whoever nominates them, and a deployment key as itself nominates nobody | `TestNobodyNominatesAHolderWiderThanThemselves` · `TestADeploymentKeyAsItselfWritesNoNomination` |
| What is held through a group is held, and may be passed on | `TestAGroupHeldPermissionIsYoursToPassOn` |
| The role everybody holds names only what is somebody's own to do | `TestTheRoleEverybodyHoldsNamesOnlyWhatIsTheirOwn` |

### Nobody takes away what they could not have given

`server/core/takeaway.go`, `server/core/narrow.go`.

| | held to it by |
| --- | --- |
| A binding erased, a role erased or narrowed, a group erased, somebody taken out of a group or a team: what the row gave, at the scope it gave it | `TestNobodyTakesAwayWhatTheyCouldNotHaveGiven` · `TestNobodyTakesAwayWhatARowGaveThem` |
| A narrowing takes out what is named and adds nothing | `TestANarrowTakesOutWhatIsNamedAndAddsNothing` |
| A team erased is every role held in it, taken | `TestATeamIsErasedByWhoeverCouldHaveGivenWhatItGives` |
| A group's manager holds what the group is bound to by being in it, and so may erase it | `TestWhoeverManagesAGroupMayEraseIt` |
| What a row gave is read from the row: a role of the whole tenant, which somebody who erases in one site does not see, is still what its binding gave there, and what a place on a team or a manager's place in a group carried | `TestWhatARowGaveIsReadFromTheRowAndNotAsItsTakerSeesIt` |
| A row that gives nothing any more -- its role erased, or the team or the group it is of -- is taken away by whoever may erase where it is, and a site is emptied of one like any other | `TestWhatGrantsNothingAnyMoreIsTakenAway` · `TestAnOperatorTakesAwayWhatGrantsNothingToo` |
| A grant is made once: one role to one person or one group in one place is one row, under two callers at once too and at the operators' port, so that one taken away is taken away | `TestAGrantIsMadeOnce` · `TestAnOperatorMakesAGrantOnceToo` |

### Nobody writes a way into an account wider than their own, or takes one away

A way in is whatever signs somebody in or acts as them. It is written, and
taken away, by somebody who holds everything the person holds -- and by the
person (`server/core/escalate.go`, `mayReach`).

| | held to it by |
| --- | --- |
| A password issued, a lockout cleared, a second factor enrolled | `TestNobodyWritesTheCredentialOfSomebodyWiderThanThey` · `TestUnlockIsHeldToTheRuleResetIs` · `TestNobodyEnrolsASecondFactorOnSomebodyWiderThanThey` |
| A password is **set** by its own person alone, who says the current one, and that ends every sign-in the old one opened; anybody else's is issued, and shown once | `TestAPersonChangesTheirOwnPassword` · `TestChangingOnesOwnPasswordEndsWhatTheOldOneOpened` |
| A second factor of one's own is taken off only from a sign-in a few minutes old, by the credential the call came with; somebody else's is the rule above | `TestTakingOnesOwnSecondFactorOffTakesAFreshSignIn` |
| And *when* is what that credential says: not a delegation beside a session, not a second key, and not a sign-in that was since ended | `TestWhatSaysWhenSomebodySignedInIsWhatTheCallCameWith` · `TestASignInFromBeforeEverythingWasEndedIsNotFresh` |
| An account at a provider, an address, a number | `TestNobodyWritesAWayInForSomebodyWiderThanThey` · `TestSomebodyAddsAWayIntoTheirOwnAccount` · `TestANumberIsSomebodysAsAnAddressIs` |
| A subject moved to another claim | `TestAMoveIsAWayInAndIsHeldToIt` |
| A front door's link of somebody's first sign-in is such a write: somebody it matched who holds more than it is not linked, and is told that an administrator links them rather than answered a failure | `TestSomebodyTheDoorMayNotLinkIsToldWhoCan` · `TestSomebodyTheAccountAppMayNotLinkIsToldWhoCan` |
| Nor is somebody it matched who already signs in through that provider by another account: a second account at one provider is refused, and they are told so, and that an administrator says which is theirs | `TestAnAddressOfSomebodyWithAnotherAccountThereIsARefusal` · `TestAnAddressOfSomebodyWithAnotherAccountThereIsARefusalHereToo` |
| A key on somebody's account | `TestNobodyMintsAKeyOnSomebodyElsesAccount` · `TestNobodyMintsAKeyOnSomebodyElsesHolder` |
| An address claimed onto an account | `TestNobodyClaimsAnAddressOntoAnAccountWiderThanTheirOwn` |
| Any of them erased | `TestAWayInIsTakenAwayByWhoeverCouldWriteIt` · `TestARevokeReachesNobodyWider` · `TestSomebodyRemovesTheirOwnWayInAndNobodyElseS` |
| What somebody holds, for this rule, is everything that reaches them: through a team and through a group too | `TestARoleHeldThroughATeamIsStillHeld` · `TestAPermissionHeldThroughAGroupIsStillHeld` |
| What the **writer** holds is counted where they hold it: a role in a site or on a team is not one to reach the tenant's administrator by | `TestARoleOnATeamIsNotOneToBecomeSomebodyBy` |
| Somebody's keys, and where they are signed in, are read by them, and by whoever holds `HolderService/SignsIn` across the tenant | `TestAKeyIsReadByWhoseItIsAndByWhoeverIsToldTo` · `TestSomebodySeesWhereTheyAreSignedInAndEndsOne` |
| And `SignsIn` answers all of them -- every account, credential and key somebody has, and not a list's first page -- in the order each was made, which is the order the person reads their own in | `TestHowSomebodySignsInIsAllOfIt` |

### Nobody is locked out by a write

| | written in | held to it by |
| --- | --- | --- |
| A tenant is made with somebody who administers it, whole or not at all: a refusal or a failure part way leaves nothing, and the alias free -- at the data plane, at `admin.addr`, at the box, and for a tenant a file declares | `server/core/tenant.go`, `cmd/admin.go` | `TestATenantIsMadeWithSomebodyWhoCanAdministerIt` · `TestATenantMadeAtTheOperatorsPortIsWholeOrNotThere` · `TestAFilesTenantIsWholeOrNotThere` |
| No write leaves a tenant with nobody administering it -- narrowing, erasing, unbinding, suspending, whichever way | `server/core/administered.go` | `TestATenantKeepsSomebodyAdministeringIt` · `TestEveryWriteThatEndsTheLastAdministrationIsRefused` · `TestAnAdministratorThroughAGroupCounts` · `TestTwoNarrowingsCannotBothTakeTheLast` |
| Leaving is signed out everywhere and erased, both or neither: the last administrator who asks to leave is refused, and stays signed in | `server/me/me.go` | `TestTheLastAdministratorWhoAsksToLeaveStaysSignedIn` |
| Nor the deployment with nobody operating it, by a key | `server/core/administered.go` | `TestAKeyOfTheDeploymentsIsNotTheDeployment` |
| Nobody removes their last way in, and what is left is one that works: an account at a provider the tenant has a connection to, or a password where the tenant takes one | `server/core/identity.go` | `TestNobodyRemovesTheirLastWayIn` · `TestTheLastWayInSurvivesTwoCallersAtOnce` · `TestALastWayInIsOneThatWorks` |
| A tenant is not closed where nobody could undo it | `server/core/close.go` | `TestClosingIsRefusedWhereNobodyCouldUndoIt` |

A command at the box and an operator at `admin.addr` are not held to the
second: that is who puts it right. Nor is a directory suspending somebody it
removed (`TestADirectoryMaySuspendTheLastAdministrator`).

### A name is somebody's by proof

| | written in | held to it by |
| --- | --- | --- |
| An address nobody proved names nobody, and keeps nobody out -- one left on the row of somebody erased no more than another: it is claimed like any other, and whoever arrives verified at it is a stranger who takes it | `server/core/email.go` | `TestAnAddressNobodyProvedNamesNobody` · `TestNobodyCanHoldAnAddressFromItsOwner` · `TestAnAddressSomebodyErasedNeverProvedIsClaimedLikeAnyOther` · `TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArriving` · `TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArrivingHereEither` |
| A proved address does not change hands | `server/core/email.go` | `TestAProvedAddressDoesNotChangeHands` |
| Nobody says their own address was checked | `server/core/email.go` | `TestNobodyAssertsThatTheirOwnAddressWasChecked` |
| An address is vouched for by an identity of its own person | `server/core/email.go` | `TestAnAddressIsVouchedForByItsOwnPersonsIdentity` |
| What a token says of somebody is one answer: the address they proved first, which proving another does not change; what they are called now; and each of their teams' names once | `server/me/me.go`, `login/claims.go` | `TestTheAddressATokenSaysIsTheOneProvedFirst` · `TestMeSaysAddressesInTheOrderTheyWereProved` · `TestATokenNamesSomebodyAsTheyAreCalledNow` · `TestATeamOfOneNameAtTwoSitesIsSaidOnce` |
| An address proved keeps the moment it was first proved: a link confirmed again, or a directory vouching for it again, moves nothing, so proving the first address a second time does not make another the one a token says. Erased and written again, it is proved anew | `server/core/email.go` | `TestAnAddressProvedAgainIsStillTheOneProvedFirst` |
| A link that proves is spent by whoever minted it, and asking roster for one is a front door's and nobody's own | `server/core/email.go` | `TestWhatSomebodyDoesToTheirOwnAccountNeedsNoRole` |
| A subject is the identifier its provider keeps fixed, and a person has one account at a provider -- when two are added at once too | `server/core/identity.go` | `TestASubjectThatLooksLikeAnAddressIsRefused` · `TestASecondAccountAtOneProviderIsRefused` · `TestAPersonHasOneAccountAtAProviderWhenTwoAreAddedAtOnce` · `TestTwoPeopleCannotBeOneSubject` |
| No link is minted to prove an address for somebody who may not sign in -- suspended, erased, or of a closed tenant -- as none is spent for them | `server/core/email.go` | `TestNoLinkProvesAnAddressForSomebodyWhoMayNotSignIn` |
| A subject is the identifier its provider keeps fixed, and a person has one account at a provider | `server/core/identity.go` | `TestASubjectThatLooksLikeAnAddressIsRefused` · `TestASecondAccountAtOneProviderIsRefused` · `TestTwoPeopleCannotBeOneSubject` |
| A provider is named as a connection is, and a subject has no space before or after it; neither is folded into what was meant | `server/core/identity.go` | `TestAnIdentityIsHeldToAForm` |
| A GUID is one spelling where its connection names people by `oid`: kept in small letters, and found in either by a write, a sign-in and a directory | `server/front/front.go` | `TestAGUIDIsOneSpelling` · `TestASignInFindsAnOidInEitherSpelling` · `TestATokensOidIsTheSamePersonInEitherSpelling` · `TestADirectoryFindsSomebodyByEitherSpellingOfTheirId` |
| A front door that enrols makes nobody whose account roster would not keep: what a subject is refused for on its form is asked before anybody is made. And arrivals of one account take turns at a front door, so one new person arriving several times at once there is one person; at two front doors each is answered as that person, and whoever the other made is left and named in the log | `arrives/arrives.go` | `TestAFirstSignInThatCannotBeLinkedMakesNobody` · `TestAFirstSignInThatCannotBeLinkedMakesNobodyHereEither` · `TestSeveralArrivalsOfOneNewPersonAtOnceAreOnePerson` |
| A hostname is claimed by a record published for it, and the latest proof takes it -- written down in the trail of the tenant that held it, and never a name a file declares | `server/core/host.go` | `TestTheLatestProofTakesTheName` · `TestANameTakenIsOnTheTrailOfWhoeverHeldIt` |
| A name is written by its proof alone: its general writes, a claim's and a tenant's are not served, whatever `allow_general_writes` says | `cmd/serve.go` | `TestAGeneralWriteDoesNotWriteANamePastItsProof` |
| A claim of a name that has run out is replaced by making another, as the refusal of it says to; one still good is not, and another tenant's is not touched | `server/core/hostproof.go` | `TestAnExpiredClaimOfANameIsReplacedByMakingAnother` |
| The record is asked of the name's own zone, never of where an alias points; a name is one spelling, a hostname, and a lookup that failed says nothing of the resolver | `server/prove/prove.go` | `TestANameIsAskedOfItsOwnZone` · `TestATrailingDotIsTheSameName` · `TestANameThatIsNotAHostnameIsRefused` · `TestALookupThatFailedSaysNothingOfTheResolver` |

### What is written is held to a form

Asked of the value and not of the caller, so the deployment's own writes are
held to it too. The numbers are in [customers.md](customers.md#how-much-is-written-of-anybody).

| | written in | held to it by |
| --- | --- | --- |
| What is written of a row has a length -- a name, a note, labels, each field of a profile, `data` -- and a write past it is refused, by whichever verb | `server/core/form.go` | `TestWhatSomebodyWritesOfThemselvesHasALength` · `TestARowsNameNoteAndLabelsHaveALength` |
| So nobody makes a list unanswerable by what they write of themselves: a full page of people, each at every bound, is one answer | `server/core/form.go` | `TestAListOfPeopleAtTheirLongestIsAnswered` |
| An address is 254 bytes at most and a number 64 characters | `server/front/front.go` | `TestANumberAndAnAddressHaveALength` |
| So has the rest of what a caller writes: a subject at a provider, a list of methods on a role, a key and a delegation, a connection's issuer, its scopes and every identifier. An account and a key are somebody's own to write, and a full page of either at its longest is one answer; a delegation's list is refused before a password is checked or a link spent on it | `server/core/form.go` | `TestAListOfAccountsAtTheirLongestIsAnswered` · `TestAListOfKeysAtTheirLongestIsAnswered` · `TestWhatARoleAndADelegationListHasALength` · `TestTheRestOfWhatAnAdministratorWritesHasALength` · `TestAListOfMethodsHoldsEveryMethodRosterServes` |
| An address is one spelling of one mailbox -- the plain form, at a hostname, in small letters, with nothing outside ASCII before the `@` -- by every road that writes one; what somebody types is folded and looked for | `server/front/front.go` | `TestAnAddressIsOneSpelling` · `TestAnAddressIsHeldToItsFormByEveryRoad` |
| An alias is written as it is read: a write that says one with a capital or a space at either end is refused with the spelling it would have to be, for every row that has one, by `Add`, a rename and a directory alike; a reference is exact, so what is written is what is found | `server/core/form.go` | `TestAnAliasIsWrittenAsItWillBeRead` |
| What somebody *types* is folded and then looked for: the alias and the tenant at a sign-in -- `Verify`, `Delegate`, a mailed link, roster's own -- as an address is, and a name typed at the command line or for a terminal | `server/vouch/vouch.go` | `TestASignInFoldsWhatSomebodyTyped` · `TestRostersOwnSignInFoldsWhatAnOperatorTyped` · `TestTheCommandLineFoldsANameSomebodyTypes` · `TestATerminalIsCalledWhatItsKeyWillBe` |

### A connection is one a front door can use

Asked of the deployment as of a tenant: what a row has to be to sign anybody
in is the same whoever wrote it.

| | written in | held to it by |
| --- | --- | --- |
| A connection's name is an alias, its issuer an address with a host in small letters and no query or fragment, and its client has a name | `server/core/connection.go` | `TestAConnectionsNameIsHeldToAnAliasesGrammar` · `TestAConnectionsIssuerIsAnAddress` |
| It names its people by `oid` only where the provider may issue one: not at GitHub's, and not at Google's | `server/core/connection.go` | `TestAConnectionNamesNoClaimItsProviderIssuesNone` |
| A front door asks a provider in plain `http` only at a host its deployment named (`private_hosts`) | `arrives/dial.go` | `TestAProviderIsAskedInTheClearOnlyWhereTheDeploymentNamedIt` |

### A picture is one a process can afford to render

Somebody's picture is theirs to set with no role, and a front door hands one
over at a sign-in. Either is an image roster decodes whole, so what one may be
is roster's to say and not a caller's (`server/portrait/portrait.go`).

| | written in | held to it by |
| --- | --- | --- |
| An image is at most 2 MiB and 2048 pixels a side, read from its header and refused before any of it is decoded, whoever sends it | `server/portrait/portrait.go` | `TestAPictureIsNoLargerThanTheWireSays` · `TestATooLargeImageIsRefusedBeforeItIsDecoded` |
| The largest image there is costs a process under 96 MiB to render | `server/portrait/portrait.go` | `TestTheLargestPictureIsRenderedWithinABound` |
| A process renders two images at once. A call beyond those waits its turn for as long as it lasts, and one that ends waiting is answered as out of time, not as an image refused | `server/portrait/portrait.go`, `server/core/portrait.go` | `TestAFewPicturesAreRenderedAtOnceAndTheRestWait` · `TestACallThatEndedWaitingToBeRenderedIsNotAnImageRefused` |

### One tenant is not reached from another

| | written in | held to it by |
| --- | --- | --- |
| Every read and write is of the caller's tenant | `cmd/policy.go` | `TestTheTenantWallStillApplies` · `TestNoFilterCarriesARowFromAnotherTenant` · `TestWhereFromAnswersACallerOfItsOwnTenant` |
| A reference to another tenant's row names nobody | `server/core/tenant.go` | `TestNamingSomebodyInAnotherTenantNamesNobody` · `TestAnEdgeIsNotAWayThroughTheWall` |
| A row's edges are all of one tenant | `server/core/tenant.go` | `TestATeamCannotBeInAnotherTenantsSite` · `TestAClaimDoesNotCrossATenant` |
| No key or way in is minted into another tenant | `server/core/apikey.go` | `TestAKeyIsNotMintedIntoAnotherTenant` · `TestNobodyMintsAWayIntoAnotherTenant` |
| The Login App's own key, as itself, is told which tenant a name is and hears who was signed out, and reads no tenant's row: what a tenant is called it reads as the holder that tenant nominated | `cli/login.go` | `TestTheProvisionedKeyIsNarrowerThanTheRole` · `TestTheLoginAppsKeyReadsATenantAsWhoThatTenantNominated` |
| Nobody is told what another customer may call | `server/core/holder.go` | `TestNobodyIsToldWhatAnotherCustomerMayCall` |

### What a file declares is the file's

| | written in | held to it by |
| --- | --- | --- |
| A row a file declares is read-only at every port: not edited, not renamed, not erased, not narrowed, not widened. Signing a declared holder out everywhere edits nothing of it, and is not refused | `server/core/declared.go` | `TestARowAFileDeclaredIsWrittenFromThere` · `TestARowAFileDeclaresIsNotRenamed` · `TestADeclaredRoleIsNobodysToNarrow` · `TestADeclaredRoleIsNobodysToWiden` · `TestAFrontDoorsOwnRowsAreDeclared` |
| A file declares a row of the tenant it names, and takes nobody else's | `cmd/resources.go` | `TestAFileDeclaresANameOfItsOwnTenant` |
| Nobody at a port says a file declared a row | `cmd/undeclared.go` | `TestNobodyAtAPortSaysAFileDeclaredARow` |
| A file does not close a tenant and does not bring one back: what it declares of a tenant that is closed or destroyed, and under one, is left as it is, and the log says at each start which files still declare it. The server starts | `cmd/resources.go` | `TestAFileDoesNotBringATenantBack` |
| A declared tenant is not renamed, at the box either: its alias is what its file finds it by | `server/core/tenant.go` | `TestADeclaredTenantIsNotRenamedAtTheBox` |

### What is gone grants nothing

| | written in | held to it by |
| --- | --- | --- |
| An erased role, an erased team | `cmd/policy.go` | `TestAnErasedRoleGrantsNothing` · `TestATeamErasedHandsOutNothing` |
| And an erase writes the row it names and no other: what hung on it -- a place on the team, in the group or the site, a binding, a relation -- is still listed, pointing at something that is not read | `server/bare` | `TestWhatHungOnSomethingErasedIsStillListed` |
| A site is erased when nothing is in it | `server/core/tenantwide.go` | `TestASiteIsErasedWhenNothingIsInIt` |
| And when two act at once: of an erase and a team, a role, a group or a binding put in the site in the same moment, one is refused; and what is in a customer's site is counted at the operators' port too | `server/core/tenantwide.go` | `TestASiteIsErasedWithNothingInItByTwoCallersAtOnce` · `TestAnOperatorErasesASiteWhenNothingIsInItToo` |
| Somebody suspended, somebody erased, a tenant closed: no credential of theirs works, and a stream they left open ends | `cmd/auth.go` | `TestASuspendedHolderReachesNothing` · `TestAnErasedHolderIsStoppedAtTheWire` · `TestAClosedTenantLetsNobodyIn` · `TestAStreamLeftOpenIsAskedAgainWhoOpenedIt` |
| Somebody suspended has been since they were first suspended: suspending them again moves nothing but whose suspension it is | `server/core/holder.go` | `TestASecondSuspensionKeepsTheFirstMoment` |
| Nothing is written into a tenant that is destroyed, by anybody: no row of a kind that names its tenant is added under it, and what it says about itself is not written back. Its alias given back and an operator's hold are still written on its row. A closed tenant is still the deployment's to write in | `server/core/tenant.go` | `TestNothingIsWrittenIntoATenantThatIsGone` |
| Nor by roster's own front doors, which pass a tenant that is being destroyed by at start | `cli/login.go` | `TestAFrontDoorIsNotAnsweredInATenantThatIsGone` |
| Somebody erased whom a policy file names is granted nothing by it, and the file is still one a server starts on: the log says who to take out of it, at a start and again when who they are changes. A subject no row has is a file that is refused | `cmd/policy_file.go` | `TestSomebodyErasedDoesNotStopTheDeploymentFromStarting` · `TestFilePolicyJoinsEveryPermissionReader` |
| What ends every sign-in of somebody's -- a password issued or changed, *sign out everywhere* -- ends a link out for them and a sign-in half made, a tie included; a link that confirms an address is not ended, and confirms nothing for somebody who may not sign in | `server/vouch/link.go` | `TestALinkEndsWithWhatEndsASignIn` · `TestASignInBegunBeforeEverythingWasEndedDoesNotFinish` · `TestALinkOfSomebodyErasedIsOneThatWasNeverThere` |
| Nor does an app in front hear otherwise, nor a key whose holder is the deployment's own; and an app suspended or erased in a tenant acts as nobody there, whatever delegation it holds | `server/keys/delegate.go` | `TestASuspendedPersonIsSuspendedInFrontOfEveryApp` · `TestAnErasedHoldersKeyStandsForNobody` · `TestADelegationStopsWithItsAppsStandingInTheTenant` |
| Nothing of somebody erased is read, by naming them or through a row that outlived them | `server/bare/holder.g.go` | `TestNothingOfAnErasedHolderIsReadableByNamingThem` · `TestNothingOfAnErasedHolderIsReadableThroughARowThatOutlivedThem` |
| Somebody forgotten is a row that says nothing its schema declares personal: no name, no profile, no picture, and nothing their directory called them | `server/forget/forget.go` | `TestSomebodyForgottenIsABlankRow` |
| A link that names an address is destroyed with the address: a claim of one whose holder is forgotten signs nobody in | `server/forget/forget.go` | `TestALinkThatProvesAnAddressGoesWithTheAddress` |
| A restore gives back the account and not the sign-ins: everything issued to somebody before it is void, as after *sign out everywhere*, whoever erased them; a key is not ended by it, and is revoked while its holder is erased | `server/forget/forget.go` | `TestARestoreGivesBackTheAccountAndNotTheSignIns` · `TestAPersonLeavesWhereTheirTenantOffersIt` · `TestARestoreIsHeardAsItIsMade` |
| A name an erase freed is whoever has it now: a restore of the one who left is refused, saying who that is and what makes way | `server/forget/forget.go` | `TestARestoreSaysWhichNameIsTaken` · `TestARestoreSaysWhenTheirDirectoryCallsSomebodyElseThatNow` |
| A tenant's destruction that stopped is finished by the next pass of the sweep, wherever it stopped: a stamped tenant is found by what is left of it, and what is written into one afterwards is taken too | `server/forget/tenant.go` | `TestADestructionThatStoppedAfterItsLastPersonIsFinished` |
| A destroyed tenant's alias is freed when its hold is over, whatever the alias is | `server/forget/tenant.go` | `TestAnAliasThatBeginsGoneIsFreedOnTheClock` |
| Somebody erased is not read: a reference that names them reaches no row, and a row that outlived them is read without them -- their identifier, and nothing of who they were. What outlived them is still the tenant's to list, filtered to them too: their addresses, their numbers and their accounts at providers, until an administrator erases each or they are forgotten | `server/bare/holder.g.go` | `TestNothingOfAnErasedHolderIsReadableByNamingThem` · `TestNothingOfAnErasedHolderIsReadableThroughARowThatOutlivedThem` · `TestAnAdministratorLetsGoOfWhatSomebodyErasedHolds` |
| An erase stamps the person alone. Their alias comes free at once; their addresses and their accounts at providers stay theirs until they are forgotten, for a restore to give back. A claim that reaches them reaches nobody, as one nobody has | `server/vouch/delegate.go` | `TestAClaimOfSomebodyErasedReachesNobody` |
| Arriving at a front door by the account they had, or verified at an address they proved, they are told the account was deleted and that an administrator brings it back or lets it go, under every `enrol`: nobody is made in their place, and an address the provider did not check is told nothing | `arrives/arrives.go` | `TestSomebodyErasedIsToldTheirAccountWasDeleted` · `TestSomebodyErasedIsToldTheirAccountWasDeletedHereToo` |
| What they still hold is let go by whoever could have taken it while they were there -- an address, a number, an account at a provider, which is nobody's last way in -- and is then somebody else's to have | `server/core/identity.go` | `TestAnAdministratorLetsGoOfWhatSomebodyErasedHolds` |
| A name erased answers nobody at roster's own sign-in, a closed tenant's says so, and a request that names two is about neither; a front door may go on answering for a name it resolved for up to a minute, and the Login App then asks again which tenant the name is and what that tenant's alias is | `cmd/hosted.go`, `login/at.go` | `TestANameErasedAnswersNobodyAndANameMovedAnswersWhoHasIt` · `TestRostersOwnDoorReadsANameAsAFrontDoorDoes` · `TestANameThatMovedSignsInTheTenantItMovedTo` · `TestATenantsNewAliasSignsItsPeopleInOnceAMinuteHasPassed` |
| Nor is somebody whose sign-in ended named to the issuer: the Login App asks roster who is there before it tells Hydra, answers a login challenge only with the sign-in made at it, and grants no consent for a subject it cannot read, whatever the client asked for | `login/login.go` | `TestAnAcceptAsksRosterWhoIsThere` · `TestASignInAnswersOnlyTheChallengeItWasMadeFor` · `TestASessionOfOneTenantDoesNotAnswerAnothersChallenge` · `TestAConsentWhoseSubjectCannotBeReadIsRefused` |
| A session a front door ends is ended at roster: the delegation it held is revoked in the session's own tenant, whichever request ended it; and a sign-out through the issuer ends only the session of whoever it is about | `frontdoor/frontdoor.go` | `TestEveryEndingRevokesTheDelegation` · `TestASessionIsEndedInItsOwnTenant` · `TestASignOutEndsOnlyItsOwnSubjectsSession` |
| A name erased answers nobody at roster's own sign-in, a closed tenant's says so, and a request that names two is about neither; a front door may go on answering for a name it resolved for up to a minute | `cmd/hosted.go` | `TestANameErasedAnswersNobodyAndANameMovedAnswersWhoHasIt` · `TestRostersOwnDoorReadsANameAsAFrontDoorDoes` |
| What ends somebody's sign-ins at roster ends them at the issuer the Login App fronts, where an instance of it heard: carried to Hydra until Hydra takes it, once, and ahead of the same person's next sign-in. One made while no instance listened for more than ten minutes is not carried: a refresh token for them then lives until its lifetime, and a browser Hydra still remembers is asked for the form all the same | `login/sync.go` | `TestSigningSomebodyOutEverywhereReachesHydra` · `TestASignOutHydraWouldNotTakeIsTriedUntilItDoes` · `TestASignOutActedOnLongAgoIsNotActedOnAgain` · `TestAStreamThatCameBackSignsNobodyOutAgain` · `TestASignOutMadeWhileNobodyListenedIsCarried` · `TestASignInIsToldToHydraAfterTheForgettingItFollows` · `TestABrowserThisAppForgotIsAskedAgain` |

### What a directory may do

A tenant's directory holds a key, and a key on the internet is what these are
written against (`server/core/directory.go`).

| | held to it by |
| --- | --- |
| It makes people, and only new ones; somebody already here is matched and not made again | `TestADirectoryMakesPeopleAndOnlyNewOnes` · `TestSomebodyAlreadyHereIsMatchedAndNotMadeAgain` |
| It suspends who signs in through its connection, and lifts only a suspension it made | `TestADirectoryLiftsOnlyItsOwnSuspension` · `TestTheDirectoryDoesNotLiftAnOperatorsSuspension` · `TestSomebodyMatchedIsNotSuspendedByTheirDirectoryUntilTheyAreLinked` |
| An operator's suspension that answered OK stands, whatever the directory says at the same moment: what the directory says of somebody's standing is read, decided and written as one act, behind a lock on their row | `TestADirectorysWordAndAnOperatorsSuspensionAtOnce` |
| What it says of somebody already here is kept and believed by nothing | `TestWhatADirectorySaysIsKeptAndBelievedByNothing` |
| It provisions through one connection a tenant, and through its organisation's own door | `TestATenantProvisionsThroughOneConnection` · `TestADirectoryProvisionsThroughItsOrganisationsOwnDoor` |
| Who has an account at that connection is its to say: nobody writes themselves one there, and one for somebody already here takes `IdentityService/Add` held across the tenant, which a front door that saw them sign in holds | `TestADirectoryFindsNobodyByAnAccountTheyWroteThemselves` · `TestAFrontDoorLinksSomebodyAtTheConnectionTheirDirectoryProvisionsThrough` |
| Nor does anybody take their own away there, or move one, and so leave their directory's hands: an erase, an *unlink* and a move each take their verb held across the tenant, not a grant made in a site | `TestSomebodyDoesNotTakeThemselvesOutOfTheirDirectorysHands` · `TestAnAccountAtTheDirectorysConnectionIsTakenAwayAcrossTheTenant` |
| It writes the numbers of whoever holds no more than its key does | `TestADirectoryLeavesTheNumbersOfSomebodyWiderThanIt` |

### What only the deployment writes

| | written in | held to it by |
| --- | --- | --- |
| Which of the deployment's secrets a connection sends, and to which issuer | `server/core/connection.go` | `TestAConnectionsSecretGoesWhereTheDeploymentSays` |
| A tenant's Slack secret | `server/core/frontdoor.go` | `TestASlackReferenceIsTheDeploymentsToWrite` |
| A connection going back to the claim it named people by before | `server/core/connection.go` | `TestAConnectionNamesItsPeopleByOneClaim` |

### At which port

| | held to it by |
| --- | --- |
| `server.addr`: walled and gated; the general writes (`Patch`, `Apply`) are closed, and each entity's own verbs are the writes there are | `TestUpdateIsTheNarrowWrite` |
| No port serves a general write, of any entity, and it is not a setting: a file that says `allow_general_writes` is refused at start | `TestNoGeneralWriteIsServed` · `TestAFileThatOpensGeneralWritesIsRefused` |
| No port serves a tenant's `Erase`, and the box offers no command for it: a tenant is closed, and a closed one is destroyed from a shell on the box. Asked anyway, the answer is that sentence, and never the database's | `TestATenantIsNotErased` · `TestTheBoxOffersNoCommandThatErasesATenant` |
| No port answers a verifier -- a key's hash, a password's -- nor does the trail | `TestTheAdminListenerAnswersNoVerifier` · `TestNoVerifierReachesTheTrail` · `TestAKeyListCarriesNoVerifier` |
| No port takes a verifier from a caller either: a credential's general writes are shut by name, whatever `allow_general_writes` says, as a key's and a delegation's are -- the port that manages the deployment's own keys included | `TestAGeneralWriteDoesNotOpenACredentialsRow` · `TestAGeneralWriteDoesNotOpenAKeysRow` |
| No port takes a moment the server stamps, of any entity: when a row was made, when somebody was suspended, when their sign-ins were ended. A write made in process -- the deployment's own, at start or from a shell on the box -- may say one | `TestNobodyAtAPortSaysWhenARowWasMade` · `TestAnOperatorSaysNoMomentEither` |
| `control.addr`: a key calls what its holder is bound on the control plane | `TestAKeyAtTheControlListenerIsWhoseItIs` |
| `admin.addr`: no wall, and the rule about whose way in is written is not asked. It is the deployment's root, and said to be ([operating.md](../operating.md)) | `TestAnOperatorAdministersCustomers` |
| `admin.addr` writes as one what the data plane writes as one: a tenant, a directory's person, an address that moves from one person to another -- and two operators asking at once do not take somebody's last two ways in | `TestATenantMadeAtTheOperatorsPortIsWholeOrNotThere` · `TestTheOperatorsStackWritesAsOneWhatTheDataPlaneDoes` |
| What needs no credential is named one method at a time, and a password is not checked for nobody | `TestNobodyVerifiesAPasswordAnonymously` · `TestIntrospectIsNotPublic` |
| A session is minted when nothing is left to prove: roster's own sign-in asks somebody's second factor, on both planes | `TestRostersOwnSignInAsksTheSecondFactor` |
| The two planes are two databases: a deployment that names one for both is refused, so that a customer's session is never an operator's | `TestTwoPlanesAreTwoDatabases` |

### At the box, of the trail

Not a call's: what an operator at a shell on the box may rely on of the
commands that move and destroy the trail ([operating.md](../operating.md) §
*The audit trail*).

| | written in | held to it by |
| --- | --- | --- |
| A dry run changes nothing, with a window of the operator's own or with none: `trail prune --dry-run` says what the policy's pass would take, and takes nothing | `cli/trail.go` | `TestADryRunOfThePolicysPassChangesNothing` |
| A flag the road taken does not read is refused by its name: `--kind`, `--to` and `--discard` with no window. And what is refused of a discard is what was said twice, `--discard` beside `--to`; alone it is taken whatever archive the deployment names, and leaves what a legal hold is on | `cli/trail.go` | `TestAFlagThePolicysPassDoesNotReadIsRefusedByName` · `TestADiscardByHandIsTakenBesideAnArchive` |
| A window of nothing is everything, and is refused in both spellings: `--older-than 0`, and a `--before` that has not come yet | `cli/trail.go` | `TestAWindowThatEndsInTimeToComeIsRefused` |

## What roster does by itself

Not refusals. These are rows roster writes beside the one a caller asked for,
each because the row alone would have been unfinished. They are opinions in
the plainest sense: nobody configured them, and they are undone by the same
writes anybody would use.

| | written in | held to it by |
| --- | --- | --- |
| A deployment's first operator is bound every method, on the control plane | `cmd/init.go` | `TestInitSeedsAnOperator` |
| A tenant is made with `admin` in it, a role `everything` that holds every method and is bound to them, and a role `self-service` that nobody is bound | `server/core/tenant.go` | `TestATenantIsMadeWithSomebodyWhoCanAdministerIt` · `TestWhatSomebodyDoesToTheirOwnAccountNeedsNoRole` |
| **Whoever makes a group manages it**: somebody of the tenant who makes a group is put in it, holding the tenant's `group-manager` role there. A group the deployment makes has no manager | `server/core/group.go` | `TestWhoeverMakesAGroupManagesIt` |
| The tenant's `group-manager` role is made the first time a group needs it, and one that is there already is used as it stands -- or refused, where it names what a manager may not hold | `server/core/group.go` | `TestATenantsOwnManagersRoleIsTheOneAMakerHolds` |
| A directory's `Provision` makes the person, their identity and their address in one write, or none of them | `server/core/directory.go` | `TestADirectoryMakesPeopleAndOnlyNewOnes` |
| A rule that counts other rows under two callers at once takes a write on a row both of them name -- the person's, the tenant's, the site's -- and that write changes nothing of the row: it is as the trail keeps it, and `roster trail rows` finds nothing in it | `cmd/policy.go` | `TestALockLeavesItsRowAsTheTrailKeepsIt` |
| A session at roster's own door is on the trail of the plane it is a row of: made by a sign-in, its idle clock moved, ended by a sign-out -- each by whoever it is of, under their tenant -- and collected by the sweep. A line says which session and when, and nothing of the cookie; and `roster trail rows` finds nothing in one | `server/session` | `TestASessionMadeAtRostersOwnDoorIsOnTheTrail` · `TestAnOperatorsSessionIsOnTheControlPlanesTrail` |
| A line of the trail that says a row went -- collected by a sweep, taken with somebody forgotten or with a tenant destroyed, a consent past its keep -- is filed under the tenant the row was of: that tenant reads it, and a hold on the tenant keeps it beside the lines that say the row was there | `server/expire` | `TestWhatASweepCollectsIsSaidUnderTheTenantItWasOf` · `TestWhatGoesWithAPersonOrATenantIsSaidUnderThatTenant` · `TestAHoldKeepsTheLineThatSaysARowWent` |
| When a key was last used is a line of its own, `roster.keys.use`, by nobody and at most once an hour for a key: no line about a key is under the verb of the call the key was presented for | `server/keys/keys.go` | `TestAKeysLastUseIsALineOfItsOwn` |

## What a tenant or a deployment switches

Where a call is decided by something somebody set. A tenant's are rows of the
tenant, written by whoever holds the write across it; a deployment's are its
configuration.

| | set by | where | held to it by |
| --- | --- | --- | --- |
| What somebody does to their own account: any of it, or none | the tenant | the methods of its `self-service` role | `TestWhatSomebodyDoesToTheirOwnAccountNeedsNoRole` |
| What a group's manager may do to their group | the tenant | the methods of its `group-manager` role | `TestATenantsOwnManagersRoleIsTheOneAMakerHolds` |
| Whether somebody may delete their own account. Somebody a directory provisions does not, whatever it says | the tenant | `config.leave` | `TestAPersonLeavesWhereTheirTenantOffersIt` · `TestSomebodyTheirDirectoryProvisionsDoesNotLeaveOnTheirOwn` |
| Whether a password is a way in | the tenant | `config.password` | `TestATenantSaysWhetherAPasswordIsAWayIn` |
| Whether somebody arriving through a provider for the first time is made, or refused | the deployment | a front door's `enrol` | `TestAStrangerIsRefusedUnlessTheDeploymentEnrols` · `TestAStrangerIsEnrolledWhereTheDeploymentSaysSo` |
| Whether a terminal is signed in from a browser | the deployment | `account.terminal` | `TestADeploymentThatDoesNotSignTerminalsInSaysSo` |
| Whether the issuer's consent hop draws a screen. A device's flow is drawn one, whatever it says | the deployment | `login.consent` | `TestTheConsentScreenIsDrawnWhenTheDeploymentAsksForOne` · `TestADeviceIsGrantedOnAScreen` |
| How many wrong secrets lock an account, and for how long | the deployment | `vouch.lockout` | `TestTheLockoutIsTheDeploymentsNumbers`, `TestTheConsolesSignInCountsByTheDeploymentsLockout` |
| How long a stream left open goes before whoever opened it is asked again | the deployment | `stream.recheck` | `TestAStreamLeftOpenIsAskedAgainWhoOpenedIt` |
| What somebody holds beyond their rows | the deployment | a file ([file-policy.md](file-policy.md)) | `TestFilePolicyJoinsEveryPermissionReader` |

## What is not a rule, on purpose

Each of these was looked at and left. They are where a deployment that wants
otherwise would say so. What some of them let through is told, with an
example each, in [wide-grants.md](wide-grants.md).

| | why |
| --- | --- |
| **Suspending somebody, lifting it, and signing them out everywhere ask no reach.** Somebody who may `Disable` suspends an account wider than their own | it stops them and makes nobody them; somebody on call has to be able to stop a compromised account (D26). The last administrator is still kept |
| **Renaming somebody** asks no reach | a name is not a way in |
| **Somebody's own `Update` writes their whole profile**, their department, their employee number and an app's `data` among it | it is one verb and a tenant switches it whole (`self-service`); a directory writes its own again when it next sends them, and no rule reads any of it. Splitting the verb is a change to the wire that was looked at and left ([#279](https://github.com/lesomnus/roster/issues/279)) |
| **`TenantService/Update` cannot take away a tenant's last label** | a map with nothing in it is absent on the wire; done at the box until a field says *none* |
| **Two people who hold the same reach each other**, and somebody who holds nothing is reached by anybody who holds the method where they are | the rule guards what somebody may do, and not who they are. It is why a role naming a person's verb, bound across the tenant, is for managing other people |
| **Leaving a group or a team by oneself** | not something somebody does to their own account; decided 2026-11-15 |
| **An account at a provider is written onto the caller's own row with no proof the subject is theirs** -- except at the connection their tenant's directory provisions through, where it is refused (*What a directory may do*) | the account page links a second directory as the person. To be read with signing in ([#275](https://github.com/lesomnus/roster/issues/275)) |
| **A key that names what is somebody's own to do** still asks for a binding | the rule about a key's methods reads bindings |
| **A key or a delegation that lists `ApiKeyService/Issue` mints up to what its holder holds**, and its own list is not asked | the list narrows what the credential *calls*; what it hands out is its holder's, as for `BindingService/Add`. It is how a terminal is signed in -- the account page's delegation mints the key `roster sign-in` asked for. A key that has to be narrow does not list `Issue` ([wide-grants.md](wide-grants.md) § *A key that mints keys*); `TestAKeyThatMintsKeysMintsWhatItsHolderHolds`. Decided 2026-11-16 |
| **A connection's writes ask nothing about who arrives through it.** Somebody who may `Add` or `Update` one decides who its people are, an administrator among them | whoever says where a tenant's people are asked who they are answers for them, in any system that asks somewhere else. It is held across the tenant, the deployment's secret is not a tenant's to send, and it is on the page of wide grants (`TestAPageIsToldWhatAGrantHandsOver`) |
| **An identifier the caller chose on `Add`** is taken, and says whether it was free | decided 2026-11-15 |
| **A `Relation`** is written and erased by whoever the role lets | no rule reads one |
| **A second factor** is not a way in on its own | it is asked for after somebody has said who they are |

## What the list shows

Read for what a hook could carry instead, there are three sorts of row above.

**Safety, and staying.** Everything under *hands out*, *takes away*, *a way
in*, *locked out*, *by proof*, *another tenant*, *a file declares* and *gone*.
A deployment that could turn one off would have a way to become somebody, or
to lose the tenant. A hook only refuses, so it could not turn one off anyway;
that is the same sentence from the other side.

**Scope, and fixed.** The five places, and what is the tenant's as a whole.
They are opinions -- a site's administrator does not make a person; a team's
administrator adds the people of the team's site -- and they are the ones a
page is drawn against, which is why they are fixed before one is. Finer than
these is the hook's.

**Opinion written into roster, and worth a second look.** A hook cannot relax a
rule, nor undo a row roster wrote, so one of these that a reasonable deployment
would want otherwise has to come out of the code -- or become a switch above --
to be anybody's choice. Each is a row of the lists above:

| | what it costs somebody who wants otherwise | proposed |
| --- | --- | --- |
| A number is held to what an address is | a directory does not write the number of anybody who holds more than its key | decided; said here because it is the one a deployment cannot take back |
| A person has one account at a provider | somebody with two accounts at one provider links one | keep: the second is nearly always a link that found the wrong row, and the refusal says so |
| Whoever makes a group manages it (*What roster does by itself*) | a deployment that wants groups made for others to manage unmakes the maker's place | keep: it is a write and not a refusal, so a hook could not carry it either way |
| A team's administrator adds only the people of the team's site | a tenant with people of no site puts them in one first | keep: it is what a grant made in a site reaches, and not a rule of its own |
| A site makes and erases no people | a regional office that hires asks the tenant | keep, and to be said again if a tenant asks |
| Somebody's keys are read by them and by whoever holds `SignsIn` across the tenant | a site's helpdesk does not list a person's keys | keep: reads are not a hook's |

And one in the other direction -- things roster has no rule about that a
deployment is likely to want one for, and would say in a hook: suspending
somebody wider than oneself; a colleague reaching a colleague; which groups
their people may leave; who may take whom out of a group that grants at a
relying party.
