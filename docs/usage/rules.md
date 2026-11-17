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
| A key on somebody's account | `TestNobodyMintsAKeyOnSomebodyElsesAccount` · `TestNobodyMintsAKeyOnSomebodyElsesHolder` |
| An address claimed onto an account | `TestNobodyClaimsAnAddressOntoAnAccountWiderThanTheirOwn` |
| Any of them erased | `TestAWayInIsTakenAwayByWhoeverCouldWriteIt` · `TestARevokeReachesNobodyWider` · `TestSomebodyRemovesTheirOwnWayInAndNobodyElseS` |
| What somebody holds, for this rule, is everything that reaches them: through a team and through a group too | `TestARoleHeldThroughATeamIsStillHeld` · `TestAPermissionHeldThroughAGroupIsStillHeld` |
| What the **writer** holds is counted where they hold it: a role in a site or on a team is not one to reach the tenant's administrator by | `TestARoleOnATeamIsNotOneToBecomeSomebodyBy` |
| Somebody's keys, and where they are signed in, are read by them, and by whoever holds `HolderService/SignsIn` across the tenant | `TestAKeyIsReadByWhoseItIsAndByWhoeverIsToldTo` · `TestSomebodySeesWhereTheyAreSignedInAndEndsOne` |

### Nobody is locked out by a write

| | written in | held to it by |
| --- | --- | --- |
| A tenant is made with somebody who administers it | `server/core/tenant.go` | `TestATenantIsMadeWithSomebodyWhoCanAdministerIt` |
| No write leaves a tenant with nobody administering it -- narrowing, erasing, unbinding, suspending, whichever way | `server/core/administered.go` | `TestATenantKeepsSomebodyAdministeringIt` · `TestEveryWriteThatEndsTheLastAdministrationIsRefused` · `TestAnAdministratorThroughAGroupCounts` · `TestTwoNarrowingsCannotBothTakeTheLast` |
| Nor the deployment with nobody operating it, by a key | `server/core/administered.go` | `TestAKeyOfTheDeploymentsIsNotTheDeployment` |
| Nobody removes their last way in | `server/core/identity.go` | `TestNobodyRemovesTheirLastWayIn` · `TestTheLastWayInSurvivesTwoCallersAtOnce` |
| A tenant is not closed where nobody could undo it | `server/core/close.go` | `TestClosingIsRefusedWhereNobodyCouldUndoIt` |

A command at the box and an operator at `admin.addr` are not held to the
second: that is who puts it right. Nor is a directory suspending somebody it
removed (`TestADirectoryMaySuspendTheLastAdministrator`).

### A name is somebody's by proof

| | written in | held to it by |
| --- | --- | --- |
| An address nobody proved names nobody, and keeps nobody out | `server/core/email.go` | `TestAnAddressNobodyProvedNamesNobody` · `TestNobodyCanHoldAnAddressFromItsOwner` |
| A proved address does not change hands | `server/core/email.go` | `TestAProvedAddressDoesNotChangeHands` |
| Nobody says their own address was checked | `server/core/email.go` | `TestNobodyAssertsThatTheirOwnAddressWasChecked` |
| An address is vouched for by an identity of its own person | `server/core/email.go` | `TestAnAddressIsVouchedForByItsOwnPersonsIdentity` |
| What a token says of somebody is one answer: the address they proved first, which proving another does not change; what they are called now; and each of their teams' names once | `server/me/me.go`, `login/claims.go` | `TestTheAddressATokenSaysIsTheOneProvedFirst` · `TestMeSaysAddressesInTheOrderTheyWereProved` · `TestATokenNamesSomebodyAsTheyAreCalledNow` · `TestATeamOfOneNameAtTwoSitesIsSaidOnce` |
| An address proved keeps the moment it was first proved: a link confirmed again, or a directory vouching for it again, moves nothing, so proving the first address a second time does not make another the one a token says. Erased and written again, it is proved anew | `server/core/email.go` | `TestAnAddressProvedAgainIsStillTheOneProvedFirst` |
| A link that proves is spent by whoever minted it, and asking roster for one is a front door's and nobody's own | `server/core/email.go` | `TestWhatSomebodyDoesToTheirOwnAccountNeedsNoRole` |
| A subject is the identifier its provider keeps fixed, and a person has one account at a provider | `server/core/identity.go` | `TestASubjectThatLooksLikeAnAddressIsRefused` · `TestASecondAccountAtOneProviderIsRefused` · `TestTwoPeopleCannotBeOneSubject` |
| A hostname is claimed by a record published for it, and the latest proof takes it -- written down in the trail of the tenant that held it, and never a name a file declares | `server/core/host.go` | `TestTheLatestProofTakesTheName` · `TestANameTakenIsOnTheTrailOfWhoeverHeldIt` |
| A name is written by its proof alone: its general writes, a claim's and a tenant's are not served, whatever `allow_general_writes` says | `cmd/serve.go` | `TestAGeneralWriteDoesNotWriteANamePastItsProof` |
| The record is asked of the name's own zone, never of where an alias points; a name is one spelling, a hostname, and a lookup that failed says nothing of the resolver | `server/prove/prove.go` | `TestANameIsAskedOfItsOwnZone` · `TestATrailingDotIsTheSameName` · `TestANameThatIsNotAHostnameIsRefused` · `TestALookupThatFailedSaysNothingOfTheResolver` |

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
| A row a file declares is read-only at every port: not edited, not erased, not narrowed, not widened | `server/core/declared.go` | `TestARowAFileDeclaredIsWrittenFromThere` · `TestADeclaredRoleIsNobodysToNarrow` · `TestADeclaredRoleIsNobodysToWiden` · `TestAFrontDoorsOwnRowsAreDeclared` |
| A file declares a row of the tenant it names, and takes nobody else's | `cmd/resources.go` | `TestAFileDeclaresANameOfItsOwnTenant` |
| Nobody at a port says a file declared a row | `cmd/undeclared.go` | `TestNobodyAtAPortSaysAFileDeclaredARow` |

### What is gone grants nothing

| | written in | held to it by |
| --- | --- | --- |
| An erased role, an erased team | `cmd/policy.go` | `TestAnErasedRoleGrantsNothing` · `TestATeamErasedHandsOutNothing` |
| A site is erased when nothing is in it | `server/core/tenantwide.go` | `TestASiteIsErasedWhenNothingIsInIt` |
| Somebody suspended, somebody erased, a tenant closed: no credential of theirs works, and a stream they left open ends | `cmd/auth.go` | `TestASuspendedHolderReachesNothing` · `TestAnErasedHolderIsStoppedAtTheWire` · `TestAClosedTenantLetsNobodyIn` · `TestAStreamLeftOpenIsAskedAgainWhoOpenedIt` |
| What ends every sign-in of somebody's -- a password issued or changed, *sign out everywhere* -- ends a link out for them and a sign-in half made, a tie included; a link that confirms an address is not ended, and confirms nothing for somebody who may not sign in | `server/vouch/link.go` | `TestALinkEndsWithWhatEndsASignIn` · `TestASignInBegunBeforeEverythingWasEndedDoesNotFinish` · `TestALinkOfSomebodyErasedIsOneThatWasNeverThere` |
| Nor does an app in front hear otherwise, nor a key whose holder is the deployment's own; and an app suspended or erased in a tenant acts as nobody there, whatever delegation it holds | `server/keys/delegate.go` | `TestASuspendedPersonIsSuspendedInFrontOfEveryApp` · `TestAnErasedHoldersKeyStandsForNobody` · `TestADelegationStopsWithItsAppsStandingInTheTenant` |
| Nothing of somebody erased is read, by naming them or through a row that outlived them | `server/bare/holder.g.go` | `TestNothingOfAnErasedHolderIsReadableByNamingThem` · `TestNothingOfAnErasedHolderIsReadableThroughARowThatOutlivedThem` |
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
| What it says of somebody already here is kept and believed by nothing | `TestWhatADirectorySaysIsKeptAndBelievedByNothing` |
| It provisions through one connection a tenant, and through its organisation's own door | `TestATenantProvisionsThroughOneConnection` · `TestADirectoryProvisionsThroughItsOrganisationsOwnDoor` |
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
| No port answers a verifier -- a key's hash, a password's -- nor does the trail | `TestTheAdminListenerAnswersNoVerifier` · `TestNoVerifierReachesTheTrail` · `TestAKeyListCarriesNoVerifier` |
| No port takes a verifier from a caller either: a credential's general writes are shut by name, whatever `allow_general_writes` says, as a key's and a delegation's are -- the port that manages the deployment's own keys included | `TestAGeneralWriteDoesNotOpenACredentialsRow` · `TestAGeneralWriteDoesNotOpenAKeysRow` |
| `control.addr`: a key calls what its holder is bound on the control plane | `TestAKeyAtTheControlListenerIsWhoseItIs` |
| `admin.addr`: no wall, and the rule about whose way in is written is not asked. It is the deployment's root, and said to be ([operating.md](../operating.md)) | `TestAnOperatorAdministersCustomers` |
| What needs no credential is named one method at a time, and a password is not checked for nobody | `TestNobodyVerifiesAPasswordAnonymously` · `TestIntrospectIsNotPublic` |
| A session is minted when nothing is left to prove: roster's own sign-in asks somebody's second factor, on both planes | `TestRostersOwnSignInAsksTheSecondFactor` |
| The two planes are two databases: a deployment that names one for both is refused, so that a customer's session is never an operator's | `TestTwoPlanesAreTwoDatabases` |

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
| **Two people who hold the same reach each other**, and somebody who holds nothing is reached by anybody who holds the method where they are | the rule guards what somebody may do, and not who they are. It is why a role naming a person's verb, bound across the tenant, is for managing other people |
| **Leaving a group or a team by oneself** | not something somebody does to their own account; decided 2026-11-15 |
| **An account at a provider is written onto the caller's own row with no proof the subject is theirs** | the account page links a second directory as the person. To be read with signing in ([#275](https://github.com/lesomnus/roster/issues/275)) |
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
