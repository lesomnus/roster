# How the walks reach anything, and how long they are willing to wait for it.
#
# Sourced rather than run: `flow.sh`, `itself.sh` and `behind.sh` are three
# walks against the same two rigs -- compose, and Jobs inside the cluster
# `scripts/cluster.sh` raises -- and what they are willing to wait for is one
# decision. Written three times it is one decision that drifts, and the way it
# drifts is that one walk keeps the five-minute hang below while the other two
# are fixed.
#
# # Why there is a number here at all
#
# `curl` with nothing said will wait on a name for five minutes. That is what
# #39 is about: the cluster rig went red four times in an afternoon and three
# were the rig rather than the change, two of those a Service name inside the
# cluster that had been resolving moments earlier --
#
#     curl: (28) Resolving timed out after 300288 milliseconds
#
# -- and each spent five of the job's eleven minutes waiting for an answer that
# was never coming. A gate that is red for its own reasons teaches people to
# rerun without reading, which is the state where a real failure gets rerun too.
#
# # Two of them, because a walk and a wait want different things
#
# [DIAL] is for the calls the walk **is**: a sign-in, a consent, a token
# exchange. Each is expected to work now, so a blip should be ridden out rather
# than reported.
#
#	--connect-timeout 5    bound the name and the connection, not the transfer
#	--retry 3              a blip is three attempts, not one verdict
#	--retry-connrefused    a port that is not up yet is transient too
#	--retry-delay 1        four seconds of patience in total, not forty
#
# [PROBE] is for the `until … done` loops at the top of each walk, where the
# answer *is* expected to be no for a while and the loop around it is the
# patience. Retrying inside one would multiply the loop's own budget by four
# and turn a minute of waiting into six -- so this bounds one attempt and
# nothing else.
#
# The common case there is a connection refused, which is instant and always
# was; what this adds is that a name which does not resolve is refused in two
# seconds rather than hanging the loop on its first iteration for five minutes.
# That is #21's failure, one shape along from the two above.
#
# # Why retrying is safe here, which is not obvious
#
# `--retry` retries **transient** errors and a timeout is one of them, so on
# the face of it [DIAL] would re-send a `POST` that may already have been acted
# on. It cannot, and the reason is the option that is deliberately **not**
# there: there is no `--max-time`. `--connect-timeout` bounds the name and the
# connection and nothing after it, so the only way one of these calls answers
# `28` is in the phase before a single byte was sent. A retry after that is a
# first attempt.
#
# That matters because these walks are a sign-in. A rig that quietly made two
# of any of those would be a rig that passes while the thing it checks is
# broken.
#
# No shebang: this is sourced, and a file that can be run is a file somebody
# runs.
DIAL="--connect-timeout 5 --retry 3 --retry-connrefused --retry-delay 1"
PROBE="--connect-timeout 2"
