package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/rstr"
)

// TestAFrozenRowIsRefusedAndAnyOtherLetBe is the example's one rule: what was
// labelled `frozen` before the write is refused, in words a caller is told,
// and every other row goes on.
func TestAFrozenRowIsRefusedAndAnyOtherLetBe(t *testing.T) {
	x := require.New(t)

	v, err := hook{}.Review(t.Context(), rstr.WriteReview_builder{
		Method:       rstr.HolderService_Disable_FullMethodName,
		LabelsBefore: map[string]string{"frozen": "true"},
	}.Build())
	x.NoError(err)
	x.True(v.GetRefused())
	x.NotEmpty(v.GetWhy())

	v, err = hook{}.Review(t.Context(), rstr.WriteReview_builder{
		Method:      rstr.GroupMembershipService_Add_FullMethodName,
		LabelsAfter: map[string]string{"frozen": "true"},
	}.Build())
	x.NoError(err)
	x.False(v.GetRefused(), "a row frozen by this write is not one that was frozen")
}

// TestTheManualsHookIsThisOne holds the service `docs/operating.md` prints to
// this one, which is built and run: the manual's is the type and the method,
// as written here.
func TestTheManualsHookIsThisOne(t *testing.T) {
	x := require.New(t)

	page, err := os.ReadFile("../../docs/operating.md")
	x.NoError(err)
	src, err := os.ReadFile("hook.go")
	x.NoError(err)

	at := strings.Index(string(page), "## A hook a deployment refuses a write by")
	x.Positive(at, "the manual has no section about a hook")
	block := regexp.MustCompile("(?s)```go\n(.*?)```").FindStringSubmatch(string(page)[at:])
	x.NotNil(block, "the section prints no Go")
	x.Contains(string(src), block[1], "the manual's hook is not the one that is built")
}
