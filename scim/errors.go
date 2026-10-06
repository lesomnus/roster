package scim

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The URNs a message is marked with (RFC 7644 §3.12, §3.4.2, §3.5.2).
const (
	errorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	listSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	patchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// contentType is what every answer is (RFC 7644 §3.1).
const contentType = "application/scim+json"

// problem is an answer a handler gives on purpose: a status, and the
// `scimType` the RFC names for it where it names one.
type problem struct {
	status   int
	scimType string
	detail   string
}

func (p *problem) Error() string { return p.detail }

func badRequest(scimType, detail string) error {
	return &problem{status: http.StatusBadRequest, scimType: scimType, detail: detail}
}

func notFound(detail string) error {
	return &problem{status: http.StatusNotFound, detail: detail}
}

// failed answers an error as SCIM says an error is answered, with what roster
// said of it where roster said it.
//
// Roster's codes are the meaning, and the mapping is the one the RFC's
// statuses make: a taken address or subject is `uniqueness`, a value roster
// refused is `invalidValue`, and a state that forbids the write -- somebody
// the directory deleted, an operator's suspension -- is a conflict, so the
// directory's logs say so rather than calling it a fault.
func (s *Server) failed(w http.ResponseWriter, r *http.Request, err error) {
	var p *problem
	if errors.As(err, &p) {
		fail(w, p.status, p.scimType, p.detail)

		return
	}

	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.InvalidArgument:
		fail(w, http.StatusBadRequest, "invalidValue", st.Message())
	case codes.AlreadyExists:
		fail(w, http.StatusConflict, "uniqueness", st.Message())
	case codes.NotFound:
		fail(w, http.StatusNotFound, "", st.Message())
	case codes.FailedPrecondition, codes.Aborted:
		fail(w, http.StatusConflict, "", st.Message())
	case codes.PermissionDenied:
		fail(w, http.StatusForbidden, "", st.Message())
	case codes.Unauthenticated:
		w.Header().Set("WWW-Authenticate", `Bearer realm="roster"`)
		fail(w, http.StatusUnauthorized, "", "the key was not accepted")
	case codes.ResourceExhausted:
		fail(w, http.StatusTooManyRequests, "", st.Message())
	case codes.Unavailable, codes.DeadlineExceeded:
		fail(w, http.StatusServiceUnavailable, "", "roster did not answer; try again")
	default:
		s.c.Log.ErrorContext(r.Context(), "scim: unexpected", "method", r.Method, "path", r.URL.Path, "err", err)
		fail(w, http.StatusInternalServerError, "", "something went wrong here")
	}
}

// fail writes one error.
func fail(w http.ResponseWriter, code int, scimType, detail string) {
	body := map[string]any{
		"schemas": []string{errorSchema},
		"status":  strconv.Itoa(code),
		"detail":  detail,
	}
	if scimType != "" {
		body["scimType"] = scimType
	}
	answer(w, code, body)
}

// answer writes one body.
func answer(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
