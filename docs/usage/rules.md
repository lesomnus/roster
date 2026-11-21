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
| And the name in a bind at the LDAP directory, for an app password as for a password; the connection is then bound as the name the tree has, which is what WhoAmI answers. What was sent as the password is a password whatever it begins with, and an app password only when it has the whole shape of a key -- which is then tried as a key and never as its owner's wrong password | `ldap/ldap.go` | `TestANameIsReadAsRosterReadsIt` · `TestAPasswordIsTriedAsAPassword` |

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

### What a connection to the directory may cost

Not a call's: what `roster ldap serve` holds a connection to, which is anybody's
until it has bound ([ldap.md](../ldap.md)). Each number is a constant with its
reason beside it, in `ldap/wire`, and none is a setting.

| | written in | held to it by |
| --- | --- | --- |
| A message is at most 64 KiB from a connection that has not bound and 1 MiB from one that has, read off its header and refused there, before a byte of what it claims is read; so is a length of the indefinite form, which LDAP has not. The refusal is a notice of disconnection, `protocolError`, and the connection is ended | `ldap/wire/read.go` | `TestAMessageLongerThanARequestIsRefusedOnItsHeader` · `TestAMessageSaysItsLengthBeforeItsBytes` |
| Reading a message costs what it is long: its bytes once, however deep it nests, and a node for each of its elements, of which it has at most 16 384 | `ldap/wire/read.go` | `TestAMessageNestedAThousandDeepCostsWhatItIsLong` · `TestAMessageOfMoreElementsThanARequestHasIsRefused` |
| A filter nests at most a hundred deep. One deeper is answered `protocolError` as the search it is and reaches no directory, and the connection is ended | `ldap/wire/filter.go` | `TestAFilterNestsAsDeepAsOneIsWrittenAndNoDeeper` |
| A directory has at most 1024 connections open, and 256 from one address, an IPv6 address being counted with the /64 it is in. One more is closed with nothing said, before a TLS handshake where there is one. Clients behind one proxy or one NAT are one address and have the 256 between them | `ldap/wire/listen.go` | `TestAConnectionOverTheBoundIsClosedAtOnce` |
| A connection that has not bound has thirty seconds for each thing it says, the first included; one that has bound may sit idle five minutes | `ldap/wire/listen.go` | `TestAConnectionThatHasNotBoundHasLessTimeToSpeak` |
| An accept that fails for a moment -- the process out of descriptors -- is waited out and asked again. A listener that is broken ends the directory, and under `roster serve` ends roster, as a consumer's broken listener does there | `ldap/wire/listen.go`, `cli/ldap.go` | `TestTheListenerGoesOnAfterAnAcceptThatFails` |
| A connection that has bound has at most a hundred searches running at once; one more is answered `busy` (51) and reaches no directory. One that has not bound has one thing going at a time: its search is answered before its next message is read | `ldap/wire/flight.go` | `TestASearchOverWhatAConnectionHasInFlightIsAnsweredBusy` · `TestAConnectionThatHasNotBoundHasOneThingGoingAtOnce` |
| What a connection has in flight ends with it: a client that hangs up, and one that does not read what it asked for within thirty seconds a message, is let go and its searches told to stop | `ldap/wire/wire.go` | `TestAClientThatHangsUpEndsItsSearch` · `TestAClientThatDoesNotReadIsLetGo` |
| An abandon reaches its search however soon after it is sent, and StartTLS is refused beside one: a search is counted in before it is started | `ldap/wire/flight.go` | `TestAnAbandonSentWithItsSearchReachesIt` · `TestStartTlsIsRefusedBesideASearch` |
| A bind ends what the connection had in flight before the directory is asked, and the directory has thirty seconds to answer it | `ldap/wire/wire.go` | `TestABindEndsWhatIsInFlight` · `TestABindHasADeadlineOfItsOwn` |
| A request's identifier is an INTEGER from one to `maxInt`, and not that of a search still running on the connection. One that is not is told so in a notice of disconnection, `protocolError`, and the connection is ended: no answer under it could be told from an answer to another request | `ldap/wire/message.go` | `TestAMessageIdentifierIsOneARequestMayHave` |
| A search stops being in flight when its answer is written: a client that has the answer finds the identifier free, and StartTLS not refused for it | `ldap/wire/flight.go` | `TestASearchIsCountedOutByItsAnswer` |
| A search's scope is an ENUMERATED and its size and time limits are INTEGERs of at most `maxInt`; anything else standing where one goes is `protocolError`, and the connection goes on | `ldap/wire/message.go` | `TestAFieldOfASearchIsWhatItsGrammarSays` |
| A name is read as it was written: of white space, only a space nobody escaped is dropped from around its parts | `ldap/dn.go` | `TestAValueIsReadBackAsItWasWritten` |

### What a request to the SCIM endpoint may cost

Not a call's: what `roster scim serve` holds for a request, which is anybody's
until its key is accepted ([scim.md](../scim.md)). The clocks and the body's
bound are constants with their reason beside them, in `cli/scim.go` and `scim`;
how many requests are worked on at once is `scim.concurrency`.

| | written in | held to it by |
| --- | --- | --- |
| A request's headers have ten seconds to arrive and 64 KiB to be said in, and a connection may sit idle two minutes; nothing bounds the answer | `cli/scim.go` | `TestTheEndpointsListenerHasClocks` |
| Nothing of a body is read before its key is accepted: under no key, or a key nobody has, the answer is `401` at once and the connection ends with it | `scim/scim.go` | `TestNothingOfABodyIsReadUnderNoKey` |
| A body is read once its key is accepted and before a turn is taken: it has thirty seconds and a megabyte, `408` and `413` past them, and one that is slow to come holds nothing anybody else waits for | `scim/scim.go` | `TestABodyIsWaitedForOutsideATurn` · `TestABodyPastTheBoundIs413` |
| A turn is a tenant's: `scim.concurrency` requests of one tenant's directory are worked on at once, four unless it says, and one tenant's requests do not hold another's turns. Asking roster who a key is, for a key not remembered, is held to the same number for the process. A number below nought is refused at start | `scim/scim.go`, `cmd/consumers.go` | `TestOneTenantsRequestsDoNotHoldAnothersTurns` · `TestAConcurrencyBelowNoughtIsRefusedAtStart` |
| Reading a filter, or the filter in a PATCH path, costs what it is long: it is read in one pass, whatever it says, and takes and refuses what it did | `scim/filter.go` | `TestAFilterIsReadInOnePass` · `TestWhatAFilterTakesIsWhatItTook` |
| What anybody can cause leaves no line of the endpoint's log: a bearer that cannot be a key is `401` and roster is not asked, a path asked by a method it does not take is `405` with `Allow`, and an asker who hangs up is written nothing. What a write named and is not kept is logged by the attribute's name and never by what a path said of it | `scim/scim.go`, `scim/errors.go` | `TestABearerThatIsNoTokenIsRefusedAndSaysNothing` · `TestAMethodAPathDoesNotTakeIs405` · `TestAnAskerWhoHangsUpIsNoError` · `TestTheLogNamesAnAttributeAndNotWhatAPathSaid` |

### One tenant is not reached from another

| | written in | held to it by |
| --- | --- | --- |
| Every read and write is of the caller's tenant | `cmd/policy.go` | `TestTheTenantWallStillApplies` · `TestNoFilterCarriesARowFromAnotherTenant` · `TestWhereFromAnswersACallerOfItsOwnTenant` |
| A reference to another tenant's row names nobody | `server/core/tenant.go` | `TestNamingSomebodyInAnotherTenantNamesNobody` · `TestAnEdgeIsNotAWayThroughTheWall` |
| A row's edges are all of one tenant | `server/core/tenant.go` | `TestATeamCannotBeInAnotherTenantsSite` · `TestAClaimDoesNotCrossATenant` |
| No key or way in is minted into another tenant | `server/core/apikey.go` | `TestAKeyIsNotMintedIntoAnotherTenant` · `TestNobodyMintsAWayIntoAnotherTenant` |
| The Login App's own key, as itself, is told which tenant a name is and hears who was signed out, and reads no tenant's row: what a tenant is called it reads as the holder that tenant nominated | `cli/login.go` | `TestTheProvisionedKeyIsNarrowerThanTheRole` · `TestTheLoginAppsKeyReadsATenantAsWhoThatTenantNominated` |
| Nobody is told what another customer may call | `server/core/holder.go` | `TestNobodyIsToldWhatAnotherCustomerMayCall` |
| Nor which tenants the LDAP directory serves, by trying names at it: a bind that does not work is answered by the kind of secret it sent and nothing of the name, under a suffix that is served as under one that is not, and an app password is asked about whatever the name | `ldap/ldap.go` | `TestABindIsAnsweredTheSameWhicheverTenantItGuessed` |
| A suffix of the directory's is one tenant's: two given one do not start, naming both. A tenant renamed while the directory runs keeps the suffix it had until a restart, and its people bind there by password as by app password | `ldap/ldap.go` | `TestTwoTenantsAreNotGivenOneSuffix` · `TestATenantRenamedUnderTheDirectoryStillBindsByPassword` |

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
| Nor does a connection of the LDAP directory's that they bound before: a search on one roster has not been asked about for five minutes asks whether whoever bound it would still be let -- an app password by bearing it again, a password's person by whether they are suspended, erased, or signed out everywhere since they bound -- and a connection whose binder would not is nobody's from then on, answered *bind first* as one that never bound is. A roster that cannot say is not somebody stopped. One call each time the clock runs out, and not one a search | `ldap/ldap.go` | `TestAConnectionIsWhoeverBoundItForAsLongAsTheyStillStand` · `TestAConnectionWhoseBinderStillStandsCostsOneCallAClock` · `TestWhatAHandlerKeepsOfABindIsTheConnections` |
| And a roster that cannot be asked is not a wrong password at the directory either: a bind made while it is unreachable, failing or held to a rate is answered `unavailable` or `busy`, and a search the same, in the directory's own words and never the transport's, which name where roster is dialed | `ldap/ldap.go` | `TestRosterBeingAwayIsNotAWrongPassword` |
| Every bind at the directory is one line of its log -- the client's address, the tenant by its identifier, the kind of secret, how it ended -- at one level whatever was sent, and so is a connection ended because its binder was stopped. No line holds what a client sent: no name, no password, no key | `ldap/ldap.go` | `TestABindIsOnTheLog` · `TestNoLineOfTheLogHoldsWhatAStrangerSent` |
| Somebody suspended has been since they were first suspended: suspending them again moves nothing but whose suspension it is | `server/core/holder.go` | `TestASecondSuspensionKeepsTheFirstMoment` |
| Nothing is written into a tenant that is destroyed, by anybody: no row of a kind that names its tenant is added under it, and what it says about itself is not written back. Its alias given back and an operator's hold are still written on its row. A closed tenant is still the deployment's to write in | `server/core/tenant.go` | `TestNothingIsWrittenIntoATenantThatIsGone` |
| Nor by roster's own front doors, which pass a tenant that is being destroyed by at start | `cli/login.go` | `TestAFrontDoorIsNotAnsweredInATenantThatIsGone` |
| Somebody erased whom a policy file names is granted nothing by it, and the file is still one a server starts on: the log says who to take out of it, at a start and again when who they are changes. A subject no row has is a file that is refused | `cmd/policy_file.go` | `TestSomebodyErasedDoesNotStopTheDeploymentFromStarting` · `TestFilePolicyJoinsEveryPermissionReader` |
| What ends every sign-in of somebody's -- a password issued or changed, *sign out everywhere* -- ends a link out for them and a sign-in half made, a tie included; a link that confirms an address is not ended, and confirms nothing for somebody who may not sign in | `server/vouch/link.go` | `TestALinkEndsWithWhatEndsASignIn` · `TestASignInBegunBeforeEverythingWasEndedDoesNotFinish` · `TestALinkOfSomebodyErasedIsOneThatWasNeverThere` |
| Nor does an app in front hear otherwise, nor a key whose holder is the deployment's own; and an app suspended or erased in a tenant acts as nobody there, whatever delegation it holds | `server/keys/delegate.go` | `TestASuspendedPersonIsSuspendedInFrontOfEveryApp` · `TestAnErasedHoldersKeyStandsForNobody` · `TestADelegationStopsWithItsAppsStandingInTheTenant` |
| Nothing of somebody erased is read, by naming them or through a row that outlived them | `server/bare/holder.g.go` | `TestNothingOfAnErasedHolderIsReadableByNamingThem` · `TestNothingOfAnErasedHolderIsReadableThroughARowThatOutlivedThem` |
| What is written about somebody is theirs: a key's alias and description, a credential's name, the labels of a binding, a relation and a nomination, and a nomination's name are on the trail at no level, are left out of its digest, and are answered by the row to whoever may read it. Every field of every entity says whether it is somebody's; why a tenant is held says it is not, and is on the trail in words | `proto/app/` | `TestEveryFieldSaysWhetherItIsSomebodys` · `TestWhatIsWrittenAboutSomebodyIsNotOnTheTrail` |
| Nor does a binding somebody erased left hanging hold a provisioner's name: a role bound only to a holder that was erased is bound to nobody, and `roster scim provision` -- or a front door's -- goes on as it does the first time | `cli/provision.go` | `TestProvisionAfterTheDirectorysHolderWasErased` |
| Somebody forgotten is a row that says nothing its schema declares personal: no name, no profile, no picture, and nothing their directory called them | `server/forget/forget.go` | `TestSomebodyForgottenIsABlankRow` |
| A link that names an address is destroyed with the address: a claim of one whose holder is forgotten signs nobody in | `server/forget/forget.go` | `TestALinkThatProvesAnAddressGoesWithTheAddress` |
| A restore gives back the account and not the sign-ins: everything issued to somebody before it is void, as after *sign out everywhere*, whoever erased them; a key is not ended by it, and is revoked while its holder is erased | `server/forget/forget.go` | `TestARestoreGivesBackTheAccountAndNotTheSignIns` · `TestAPersonLeavesWhereTheirTenantOffersIt` · `TestARestoreIsHeardAsItIsMade` |
| A name an erase freed is whoever has it now: a restore of the one who left is refused, saying who that is, by identifier and with no name printed, and what makes way | `server/forget/forget.go` | `TestARestoreSaysWhichNameIsTaken` · `TestARestoreSaysWhenTheirDirectoryCallsSomebodyElseThatNow` |
| Nobody is restored into a tenant that is destroyed: an account is somebody of a tenant | `server/forget/forget.go` | `TestNobodyIsRestoredIntoATenantThatIsDestroyed` |
| A hold on a tenant stops it being destroyed, its people being forgotten on the clock and its alias being given back, by the clock as by hand, and keeps what the trail has of it. It does not stop somebody of it being forgotten by name: their rows go, the trail's lines about them stay until it lifts, and the command says so. Nor does it stop the tenant's grace, which `roster hold place` says of a closed tenant | `server/forget/hold.go` | `TestForgettingSomebodyByNameUnderAHoldSaysWhatAHoldKeeps` · `TestAHoldOnADestroyedTenantKeepsItsAliasOnTheClockToo` · `TestAHoldPlacedOnAClosedTenantSaysItsGraceRunsOn` |
| `roster forget` with no name is every loop `serve` runs for it, once: people, consents, tenants, aliases and what the trail is owed. What fails is said and does not stop the rest, and the command answers with it at the end | `cli/forget.go` | `TestRosterForgetDoesWhatTheLoopsDo` |
| A forget made on the clock is made of somebody who is still due when it destroys them: whoever was restored, or whose tenant was held, after the pass read its list is left as they are, and so are their consents | `server/forget/forget.go` | `TestAPassLeavesWhoeverWasRestoredAtOnce` · `TestAPassForgetsNobodyOfATenantHeldAtOnce` · `TestAPassOfConsentsLeavesThoseOfSomebodyRestoredAtOnce` |
| A restore and a forget of one person at once leave them forgotten or back, and never between: whichever comes second is refused, saying what the first did | `server/forget/forget.go` | `TestARestoreAndAForgetOfOnePersonAtOnceLeaveThemWhole` |
| Nothing is written for somebody forgotten: a row of any kind that names a person is refused when they were forgotten while it was being written, and none is left on them | `server/forget/nobody.go` | `TestNothingIsWrittenForSomebodyForgottenAtOnce` · `TestNobodyForgottenIsBoundByTwoCallersAtOnce` |
| What a forget still owes the trail -- above all the lines a legal hold kept, once it lifts -- is paid in every deployment, by `serve` on a clock and by `roster forget` with no name, whether or not the trail has a retention window; and paying it destroys nothing else | `server/forget/owed.go` | `TestWhatTheTrailOwesIsPaidWhereItIsKeptForEver` · `TestRosterForgetPaysWhatTheTrailOwes` |
| Somebody who deletes their own account is told what is done: forgotten from a date and not on it, brought back until the pass that forgets them, put off by a legal claim, and what is kept past it -- what they agreed to, and the record by identifier | `account/account.go` | `TestSomebodyWhoLeavesIsToldFromWhenAndWhatIsKept` · `TestMeLeaveSaysFromWhenAndWhatIsKept` |
| Nobody of the control plane is forgotten, restored or held: an operator erased is not brought back and what was theirs stays, and the erase says so before it is made | `cli/control.go` | `TestAnOperatorErasedIsSaidToBeKeptAndNotBroughtBack` |
| The log of somebody erased arriving at a front door names their holder by identifier and nothing of theirs at a provider | `arrives/arrives.go` | `TestTheLogOfSomebodyErasedArrivingNamesNoSubject` |
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
| It makes people, and only new ones; somebody already here is matched and not made again, and a create that meets the person it was making finishes them rather than make a second | `TestADirectoryMakesPeopleAndOnlyNewOnes` · `TestSomebodyAlreadyHereIsMatchedAndNotMadeAgain` · `TestACreateThatMeetsThePersonItWasMakingFinishesThem` |
| It suspends who signs in through its connection, and lifts only a suspension it made. Its word that somebody left is written before anything said beside it, and stands though the rest is refused; its word that they are back is written after | `TestADirectoryLiftsOnlyItsOwnSuspension` · `TestTheDirectoryDoesNotLiftAnOperatorsSuspension` · `TestSomebodyMatchedIsNotSuspendedByTheirDirectoryUntilTheyAreLinked` · `TestADirectorysWordThatSomebodyLeftIsWrittenFirst` |
| An operator's suspension that answered OK stands, whatever the directory says at the same moment: what the directory says of somebody's standing is read, decided and written as one act, behind a lock on their row | `TestADirectorysWordAndAnOperatorsSuspensionAtOnce` |
| What it says of somebody already here is kept and believed by nothing | `TestWhatADirectorySaysIsKeptAndBelievedByNothing` |
| It says nothing of itself: what it calls somebody is refused of the caller's own row, whoever holds the verb. And what an operator links from what it said is people: `roster scim adopt` leaves out a holder an app acts as, says so, and never links it | `TestADirectorySaysNothingOfItself` · `TestAdoptListsPeopleAndNotWhatAnAppActsAs` |
| It provisions through one connection a tenant, and through its organisation's own door | `TestATenantProvisionsThroughOneConnection` · `TestADirectoryProvisionsThroughItsOrganisationsOwnDoor` |
| Who has an account at that connection is its to say: nobody writes themselves one there, and one for somebody already here takes `IdentityService/Add` held across the tenant, which a front door that saw them sign in holds | `TestADirectoryFindsNobodyByAnAccountTheyWroteThemselves` · `TestAFrontDoorLinksSomebodyAtTheConnectionTheirDirectoryProvisionsThrough` |
| Nor does anybody take their own away there, or move one, and so leave their directory's hands: an erase, an *unlink* and a move each take their verb held across the tenant, not a grant made in a site | `TestSomebodyDoesNotTakeThemselvesOutOfTheirDirectorysHands` · `TestAnAccountAtTheDirectorysConnectionIsTakenAwayAcrossTheTenant` |
| It writes the numbers of whoever holds no more than its key does | `TestADirectoryLeavesTheNumbersOfSomebodyWiderThanIt` |
| Its writes stop at once when its connection stops provisioning: what the SCIM endpoint remembers of a key, for a minute, is for reads, and a write asks again which connection provisions | `TestAWriteAsksAgainWhichConnectionProvisions` · `TestAReadInsideTheMinuteAsksRosterNothingAboutTheKey` |
| Of a group it made, `members` names only people it answers; a list of members it sends is written whole or not at all; and it takes somebody out by their place, so a restore does not put back who it took out | `TestMembersNamesWhomTheEndpointAnswers` · `TestARefusedListOfMembersChangesNothing` · `TestSomebodyErasedIsTakenOutOfAGroup` |
| Of a group it made, `members` names only people it answers; a list of members it sends is written whole or not at all; and it takes somebody out by their place, so a restore does not put back who it took out. A `remove` takes out who it names, and everybody only where it names no list at all | `TestMembersNamesWhomTheEndpointAnswers` · `TestARefusedListOfMembersChangesNothing` · `TestSomebodyErasedIsTakenOutOfAGroup` · `TestARemoveOfMembersTakesOutWhoItNames` |
| It takes over no group an operator made: a group it names alike is another, under the alias and a suffix where the alias is taken. A name one of its own groups has is refused to a create and to a rename alike | `TestADirectoryChangesTheGroupsItMadeAndNoOther` · `TestTwoOfADirectorysGroupsWhoseNamesMakeOneAliasAreTwoGroups` · `TestAGroupIsNotRenamedToWhatAnotherIsCalled` |

### What a directory over LDAP says

`roster ldap serve` reads with a key and writes nothing. These are what a
client configured against it relies on ([ldap.md](../ldap.md)).

| | written in | held to it by |
| --- | --- | --- |
| Somebody is in the directory whatever became of a group or a team they were in: a place in something erased is not named, and nothing else about them changes | `ldap/groups.go` | `TestSomebodyIsInTheDirectoryWhateverBecameOfAGroupTheyWereIn` |
| A name in a filter is compared as a name: `memberOf`, `member` and `manager` match however the name is spaced or capitalised, so a filter that keeps a group out keeps it out. A value that is not a name matches nobody, and neither does its `!` | `ldap/entry.go` | `TestANameIsComparedAsAName` |
| Somebody is called there what a token calls them: `cn` and `sn` are the display name, else the name they were added with, else the alias, one value and never missing; `displayName` is a token's `name` exactly, and absent where a token says none. Somebody renamed is found by the new name | `ldap/people.go` | `TestSomebodyIsCalledWhatATokenCallsThem` |
| The first `mail` is the address a token's `email` says: the addresses somebody proved, in the order they proved them, and none that nobody proved | `ldap/people.go` | `TestTheFirstMailIsTheAddressATokenSays` |
| A machine is not a person there: a holder a file or a provisioner declared, and the holder the directory is answered as, have no entry, are no group's `member` and nobody's `manager`. A tenant's first administrator is a person | `ldap/people.go` | `TestAMachineIsNotAPersonInTheDirectory` |
| A filter means the same whether or not it is the read roster is asked: an employee number minds case in both, and a value with nothing in it answers nobody, with success | `ldap/entry.go` | `TestAnEmployeeNumberMindsCase` · `TestAFilterThatAsksForNothingAnswersNobody` |
| What stays the same about somebody there is `entryUUID`: a rename moves their `uid` and their DN and leaves it, and a name an erase freed is the next person's with another | `ldap/people.go` | `TestAClientKeysOnEntryUUID` |
| Its tree lists every node it answers: each entry is among the children of the one above it, the suffix's own `ou=teams` included, and a subtree search of a name holds the name | `ldap/search.go` | `TestTheTreeListsEveryNodeItAnswers` |
| A page there is no bigger than the client asked, whatever part of the tree it is of, and no page is empty but the last. A size limit is of the whole search, over however many pages it is read in, and a page of zero with a cookie ends the paging. A cookie is kept nowhere but in the client's hands, and reads only the tenant its presenter is bound in | `ldap/search.go` | `TestAPageIsNoBiggerThanWasAsked` · `TestNoPageIsEmptyButTheLast` · `TestASizeLimitIsOfTheWholeSearch` · `TestAPageOfNothingEndsThePaging` · `TestACookieIsWhereRosterWasReadTo` |
| A lookup there costs what it reads, and so does what it leaves on the access record: a sign-in by name, by address, by the two in an `or` or by `entryUUID` is one or two reads of roster whatever the tenant's size, and none of anybody else; a filter that keeps nobody reads none of anybody's lists; a page of a hundred people is four calls of each list it needs | `ldap/people.go` | `TestALookupCostsWhatItReads` · `TestAnOrAnswersEverybodyOnce` |

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
| No port answers a verifier -- a key's hash, a password's -- nor does the trail, nor the access record kept beside it, of a row read by one | `TestTheAdminListenerAnswersNoVerifier` · `TestNoVerifierReachesTheTrail` · `TestAKeyListCarriesNoVerifier` · `TestNothingDeclaredSecretReachesTheRecord` |
| No port takes a verifier from a caller either: a credential's general writes are shut by name, whatever `allow_general_writes` says, as a key's and a delegation's are -- the port that manages the deployment's own keys included | `TestAGeneralWriteDoesNotOpenACredentialsRow` · `TestAGeneralWriteDoesNotOpenAKeysRow` |
| No port takes a moment the server stamps, of any entity: when a row was made, when somebody was suspended, when their sign-ins were ended. A write made in process -- the deployment's own, at start or from a shell on the box -- may say one | `TestNobodyAtAPortSaysWhenARowWasMade` · `TestAnOperatorSaysNoMomentEither` |
| `control.addr`: a key calls what its holder is bound on the control plane | `TestAKeyAtTheControlListenerIsWhoseItIs` |
| `admin.addr`: no wall, and the rule about whose way in is written is not asked. It is the deployment's root, and said to be ([operating.md](../operating.md)) | `TestAnOperatorAdministersCustomers` |
| `admin.addr` writes as one what the data plane writes as one: a tenant, a directory's person, an address that moves from one person to another -- and two operators asking at once do not take somebody's last two ways in | `TestATenantMadeAtTheOperatorsPortIsWholeOrNotThere` · `TestTheOperatorsStackWritesAsOneWhatTheDataPlaneDoes` |
| What needs no credential is named one method at a time, and a password is not checked for nobody | `TestNobodyVerifiesAPasswordAnonymously` · `TestIntrospectIsNotPublic` |
| A session is minted when nothing is left to prove: roster's own sign-in asks somebody's second factor, on both planes | `TestRostersOwnSignInAsksTheSecondFactor` |
| The two planes are two databases: a deployment that names one for both is refused, so that a customer's session is never an operator's | `TestTwoPlanesAreTwoDatabases` |
| The LDAP directory's port, before a bind: the root DSE is read and nothing else is -- a search is *bind first*, WhoAmI names nobody, and the writes, compare and password modify are refused as they are to everybody -- and the root DSE lists StartTLS only where a StartTLS on that connection would be taken | `TestBeforeABindIsWhatTheManualSays` · `TestTheRootDseListsStartTlsOnlyWhereItIsOffered` · `TestLdapServeSaysStartTlsWhereItWasGivenACertificate` |

### At the box, of the trail

Not a call's: what an operator at a shell on the box may rely on of the
commands that move and destroy the trail ([operating.md](../operating.md) §
*The audit trail*).

| | written in | held to it by |
| --- | --- | --- |
| A dry run changes nothing, with a window of the operator's own or with none: `trail prune --dry-run` says what the policy's pass would take, and takes nothing | `cli/trail.go` | `TestADryRunOfThePolicysPassChangesNothing` |
| A flag the road taken does not read is refused by its name: `--kind`, `--to` and `--discard` with no window. And what is refused of a discard is what was said twice, `--discard` beside `--to`; alone it is taken whatever archive the deployment names, and leaves what a legal hold is on | `cli/trail.go` | `TestAFlagThePolicysPassDoesNotReadIsRefusedByName` · `TestADiscardByHandIsTakenBesideAnArchive` |
| A window of nothing is everything, and is refused in both spellings: `--older-than 0`, and a `--before` that has not come yet | `cli/trail.go` | `TestAWindowThatEndsInTimeToComeIsRefused` |
| Work that logs rather than fails is said, on stderr, and what it could not do fails the command: the policy's pass (`trail prune`), and the payment of the archive's half of an erasure (`forget`). The person is still forgotten where the transaction reached, and what is left is owed | `cli/telemetry.go` | `TestAPassAtTheBoxSaysWhatItDidAndFailsWhenItCouldNot` · `TestForgettingAtTheBoxSaysWhenTheArchivesHalfIsStillOwed` |
| What an act on the archive passed over is said too -- a chunk the account does not have, left alone by a purge or left out of a check of the rows -- and is no failure of that command's: `trail verify` fails for it | `cli/trail.go` | `TestAnActOnTheArchiveSaysWhatItLeftAlone` |
| An acceptance of rows says who and why: who is the operator `--as` names, checked as it is everywhere, or the shell alone; each line it writes names that operator; the why is said on stderr and is not on the trail | `cli/trail.go` | `TestAnAcceptanceOfRowsSaysWhoAndWhy` |
| `trail owed` names a hold as keeping an erasure only while the tenant is held | `cli/trail.go` | `TestWhatTheTrailOwesSaysWhetherTheHoldIsStillThere` |
| What the two checks do **not** find is said, and the page is held to the checks. `trail rows` holds each row to the newest line about it, by that line's identifier, moment and action: a line's actor, tenant or patch rewritten, any other line rewritten or removed, a line added in the past and a write undone with its line are found by neither check. Neither reads the control plane or the access record, and a pass that could not write its archive leaves `unkept`, which only an acceptance clears | [operating.md](../operating.md) § *The audit trail* | `TestWhatNeitherCheckOfTheTrailFinds` · `TestNeitherCheckReadsTheControlPlaneOrTheAccessRecord` · `TestAPassThatCouldNotWriteItsArchiveLeavesACheckpointTheStoreNeverKept` |

And of the `audit:` block a deployment starts from, which is not a setting a
call is decided by: what is refused of it, where the process comes up.

| | written in | held to it by |
| --- | --- | --- |
| An `audit:` block that cannot be meant is refused at start, in a sentence that names the key. Two things that cannot both be meant: a discard beside a digest key, `destroy` with no `retain`, a `store:` witness inside the archive. And what nothing reads: `discard` with no `retain`, a kind's block that says nothing, `every` with no window, `hear_within` with no witness, a time that is less than none | `cmd/config.go` | `TestAnAuditBlockThatCannotBeMeantIsRefusedAtStart` |
| Which kinds a block may be written for, and whose discard a digest key refuses, follow from what the trail holds lines about, and not from a list: the trail's own tables take no block, and the access record's discard is taken | `cmd/config.go` | `TestWhatAKindsBlockMaySayFollowsFromWhatTheTrailHoldsOfIt` |
| A kind's block that says its content and no window keeps the deployment's window, and `profile: forever` is how a block says for ever | `cmd/config.go` | `TestAKindsContentAloneKeepsTheDeploymentsWindow` |
| What looked as if nothing read it, and is read, still starts: a checkpoints key with no archive, a floor, `every` where a witness waits on it. And every `audit:` block the pages show, and every file and environment this repository ships | `cmd/config.go` | `TestWhatOfAnAuditBlockIsReadAfterAllStillStarts` · `TestEveryAuditBlockThePagesShowStarts` |

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
| A call that showed, wrote or was refused somebody's personal data is a row of the access record, **under the tenant of whoever it was about**: what an operator or a deployment key read of a customer's person is the customer's, whether they read the row whole or two fields of it; a refused call is under the tenant of the row it named by identifier, another tenant's across the wall included, and under the caller's where it named nothing that exists. A hold on that tenant keeps them | `cmd/record.go` | `TestWhoReadOrWroteWhoseDataIsRecorded` · `TestAReadIsFiledUnderTheTenantOfWhoeverWasRead` · `TestAKeysReadIsFiledUnderTheTenantOfWhoeverWasRead` · `TestAReadAtTheBoxIsFiledUnderTheTenantOfWhoeverWasRead` · `TestARefusedReadAcrossTheWallIsOnTheRecordOfWhoseRowItNamed` · `TestAHoldKeepsAReadThatLeftTheTenantOut` |
| What `roster scim adopt` writes -- each account at the connection, and what it takes away of what the directory said -- is a row of it naming the operator at the shell, as the same write through `roster --as … identity add` is | `cli/scim.go` | `TestAdoptsLinksAreOnTheRecordUnderWhoeverMadeThem` |
| How somebody signs in, read by an administrator or an operator (`HolderService/SignsIn`), is a row of it naming the person and each account at a provider it answered. What somebody is told of themselves (`MeService/Get`), by themselves or through an app they are using, is **not**: it takes no subject, and is nobody reading somebody else's data. An answer that carries a personal field's name in a message of its own is named in a test with which of the two it is | `server/core/holder.go` · `server/me` | `TestReadingHowSomebodySignsInIsOnTheRecord` · `TestWhatSomebodyIsToldOfThemselvesLeavesNoRow` · `TestEveryAnswerThatNamesSomebodyIsOnTheRecordOrSaysWhy` |
| A row of it keeps how a read was asked and none of the words it was asked by: identifiers, what was selected and a page's size stay; an alias, a name, an address, a subject, a number, a fragment, a cursor and a secret do not, whatever RPC it was. So nothing declared `secret` reaches it, and what is left of somebody forgotten is identifiers | `cmd/record.go` | `TestARowKeepsHowItWasAskedAndNotTheWordsItWasAskedBy` · `TestNoWordOfAnyRequestIsKept` · `TestNothingDeclaredSecretReachesTheRecord` · `TestASecretThatLooksLikeAnIdentifierIsNotKept` · `TestWhatIsLeftOfSomebodyForgottenOnTheRecordIsIdentifiers` |
| What its writer writes in one go is written whole or not at all, so a flush that failed part way and is tried again writes each read once | `cmd/record.go` | `TestAFlushThatFailsPartWayWritesNothing` |

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
| How long a row of the access record is kept: for good until a window is named, which `serve` says as it starts for as long as none is and nobody said for good is meant. A window of its own needs no archive | the deployment | `audit.by.access` | `TestServeSaysWhenTheAccessRecordHasNoWindow` · `TestAWindowOfItsOwnBoundsTheAccessRecordWithNoArchive` |
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
