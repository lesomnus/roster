package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAGroupThatIsGoneIsNotExposed: a group that was erased is shown to no
// client, at either port, in one answer.
//
// # What it was
//
// `GroupExposureService/Add` naming an erased group was `NotFound` at the data
// port, where the wall reads the group the row hangs off, and `OK` at the
// operators' port, which has no wall: the row was written onto a group that is
// gone, and a token would have carried it the day the group came back.
func TestAGroupThatIsGoneIsNotExposed(t *testing.T) {
	for port, p := range identifierPorts(t) {
		t.Run(port, func(t *testing.T) {
			x := require.New(t)

			group := app.GroupRef_builder{Id: p.group}.Build()
			_, err := app.NewGroupServiceClient(p.conn).Erase(p.as, group)
			x.NoError(err)

			_, err = app.NewGroupExposureServiceClient(p.conn).Add(p.as, app.GroupExposureAddRequest_builder{
				Group: group, Client: "loft",
			}.Build())
			x.Equal(codes.NotFound, status.Code(err), "%v", err)
			x.Equal("Group not found", status.Convert(err).Message())
		})
	}
}
