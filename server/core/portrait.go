package core

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/portrait"
)

// A picture of somebody, in both of its forms.
//
// `profile.picture` says where a picture is, and `portrait` is roster's copy of
// one, at the sizes a screen draws (`server/portrait`). What is here keeps the
// two describing **one** picture, and keeps the copy roster's own: nothing a
// caller sends is stored as a portrait, so what a row carries is bounded by
// what roster renders and not by what somebody chose to send.

// maxDisplayName is the longest name [coreHolder.Fill] writes. A directory's
// display name is a person's name, and one longer than this is not one -- it is
// left out rather than cut, because a name cut short is a wrong name.
const maxDisplayName = 256

// Add refuses a portrait and a picture URL roster would not hand a page.
//
// A portrait is made from an image, by `Portray`; one handed over with the row
// would be whatever its caller chose to carry, which is the one thing a
// portrait is built not to be. The URL is held to the rule `Update` holds a
// changed one to, since a new row's is new.
func (s coreHolder) Add(ctx context.Context, req *app.HolderAddRequest) (*app.Holder, error) {
	if req.HasPortrait() {
		return nil, status.Error(codes.InvalidArgument,
			"portrait: roster makes one from an image, and Portray is what takes it")
	}
	if err := portrait.CheckURL(req.GetProfile().GetPicture()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "profile.picture: %s", err)
	}

	return s.HolderServiceServer.Add(ctx, req)
}

// Fill writes the blanks, and only the blanks; `holder_svc.ext.proto` says why
// it is a method of its own.
//
// The row is read and written under the version that read saw, so a person
// saving their profile in between is not overwritten by a sign-in: the write is
// refused, and the next sign-in finds the blank filled or not.
//
// # What counts as having a picture
//
// Either form. Somebody whose profile names a picture URL has chosen one, even
// with no portrait -- a URL somebody typed is not fetched, so it never has one
// -- and a portrait made from their directory would be a second picture, of
// somebody they may not want to be shown as. So a picture is filled for
// somebody with **neither**, and both halves of it at once: the URL the
// provider gave, when a browser can fetch it, and the portrait its image makes.
func (s coreHolder) Fill(ctx context.Context, req *app.HolderFillRequest) (*app.HolderFillResponse, error) {
	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref: req.GetRef(),
		Select: app.HolderSelect_builder{
			Labels:      z.Ptr(true),
			Profile:     z.Ptr(true),
			Portrait:    z.Ptr(true),
			DateUpdated: z.Ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", v.GetLabels()); err != nil {
		return nil, err
	}

	p := &app.Profile{}
	if v.HasProfile() {
		p = proto.Clone(v.GetProfile()).(*app.Profile)
	}
	patch := app.HolderPatchRequest_builder{Ref: req.GetRef(), DateUpdated: v.GetDateUpdated()}
	wrote := false

	if name := strings.TrimSpace(req.GetDisplayName()); name != "" && p.GetDisplayName() == "" &&
		utf8.RuneCountInString(name) <= maxDisplayName {
		p.SetDisplayName(name)
		wrote = true
	}

	pictureless := p.GetPicture() == "" && len(v.GetPortrait().GetRenditions()) == 0
	if pictureless {
		// Checked before anything is written, so a refusal writes no half: a
		// name filled and a picture refused would be a call that did
		// something and answered that it did not.
		where := req.GetPicture()
		if err := portrait.CheckURL(where); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "picture: %s", err)
		}
		if len(req.GetImage()) > 0 {
			kept, err := portrait.Render(req.GetImage())
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "image: %s", err)
			}
			patch.Portrait = kept
			pictureless = false
			wrote = true
		}
		if where != "" {
			p.SetPicture(where)
			pictureless = false
			wrote = true
		}
	}

	if wrote {
		patch.Profile = p
		if _, err := s.HolderServiceServer.Patch(ctx, patch.Build()); err != nil {
			return nil, err
		}
	}

	return app.HolderFillResponse_builder{Pictureless: pictureless}.Build(), nil
}

// Portray keeps an image as somebody's picture, rendered, or takes their
// picture away; either way the profile's URL goes, for the reason
// `holder_svc.ext.proto` gives.
//
// The caller's version is the one written under, as `Update` takes it: this
// replaces a value the caller saw, and the profile it rewrites may have moved
// since.
func (s coreHolder) Portray(ctx context.Context, req *app.HolderPortrayRequest) (*app.Holder, error) {
	v, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.HolderSelect_builder{Labels: z.Ptr(true), Profile: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", v.GetLabels()); err != nil {
		return nil, err
	}

	patch := app.HolderPatchRequest_builder{Ref: req.GetRef(), DateUpdated: req.GetDateUpdated()}
	if len(req.GetImage()) == 0 {
		patch.Portrait = none()
	} else {
		kept, err := portrait.Render(req.GetImage())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "image: %s", err)
		}
		patch.Portrait = kept
	}
	if v.GetProfile().GetPicture() != "" {
		p := proto.Clone(v.GetProfile()).(*app.Profile)
		p.SetPicture("")
		patch.Profile = p
	}

	return s.HolderServiceServer.Patch(ctx, patch.Build())
}

// none is a portrait taken away: one with no renditions, written as that rather
// than as nothing.
//
// Nothing would be right for the row and wrong for every page showing it. A
// page's store reads a field that is absent from an answer as a field that
// was not selected, and keeps what it had -- so a portrait cleared to nothing
// goes on being drawn by the page that cleared it. An empty one is a value, and
// replaces the old.
func none() *app.Portrait { return &app.Portrait{} }

// List answers each person's portrait at the one size a page carries; see
// [portrait.Page] for why that is safe, and `Get` for every size.
func (s coreHolder) List(ctx context.Context, req *app.HolderListRequest) (*app.HolderListResponse, error) {
	res, err := s.HolderServiceServer.List(ctx, req)
	if err != nil {
		return nil, err
	}

	items := res.GetItems()
	for i, v := range items {
		items[i] = paged(v)
	}

	return res, nil
}

// Watch is a page sent again whenever it changes, and carries what a page does.
func (s coreHolder) Watch(req *app.HolderWatchRequest, stream app.HolderService_WatchServer) error {
	return s.HolderServiceServer.Watch(req, pagedStream{stream})
}

type pagedStream struct {
	app.HolderService_WatchServer
}

// Send cuts each row down in a message of its own. What a stream is handed may
// be what every other stream watching the same rows is handed, so the message
// and its items are left as they came.
func (s pagedStream) Send(v *app.HolderWatchResponse) error {
	var out []*app.HolderWatchItem
	for i, w := range v.GetItems() {
		if len(w.GetValue().GetPortrait().GetRenditions()) < 2 {
			if out != nil {
				out = append(out, w)
			}
			continue
		}
		if out == nil {
			out = append(make([]*app.HolderWatchItem, 0, len(v.GetItems())), v.GetItems()[:i]...)
		}

		w = proto.Clone(w).(*app.HolderWatchItem)
		portrait.Page(w.GetValue().GetPortrait())
		out = append(out, w)
	}
	if out == nil {
		return s.HolderService_WatchServer.Send(v)
	}

	return s.HolderService_WatchServer.Send(app.HolderWatchResponse_builder{Items: out}.Build())
}

// paged is a row as a page carries it: itself when there is nothing to cut,
// and a copy cut down otherwise, so that whatever handed the row over keeps
// what it had.
func paged(v *app.Holder) *app.Holder {
	if len(v.GetPortrait().GetRenditions()) < 2 {
		return v
	}

	v = proto.Clone(v).(*app.Holder)
	portrait.Page(v.GetPortrait())

	return v
}
