package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// TestAFilterOfNothingIsRefused: `filter=` with nothing after it is a filter
// that says nothing, and is refused as one, `400 invalidFilter`, of people
// and of groups. A list with no `filter` at all is the list.
//
// It listed everybody. A directory that asks whether somebody is there reads
// a list of everybody as somebody else, and skips the person it asked after.
func TestAFilterOfNothingIsRefused(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	d.someone(t, "erin", erinOid)

	for _, path := range []string{"/Users?filter=", "/Users?filter=%20", "/Groups?filter=", "/Groups?filter=&excludedAttributes=members"} {
		code, got, _ := d.call(t, d.key, http.MethodGet, scim.Base+path, nil)
		x.Equal(http.StatusBadRequest, code, "%s: %v", path, got)
		x.Equal("invalidFilter", got["scimType"], path)
	}
	for _, path := range []string{"/Users", "/Groups"} {
		code, got, _ := d.call(t, d.key, http.MethodGet, scim.Base+path, nil)
		x.Equal(http.StatusOK, code, "%s: %v", path, got)
	}
}
