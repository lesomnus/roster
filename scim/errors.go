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
// said of it where roster said it, and says in the log what it refused.
//
// Roster's codes are the meaning, and the mapping is the one the RFC's
// statuses make: a taken address or subject is `uniqueness`, a value roster
// refused is `invalidValue`, and a state that forbids the write -- somebody
// the directory deleted, an operator's suspension -- is a conflict, so the
// directory's logs say so rather than calling it a fault.
//
// # What is said here as well
//
// A refusal is answered to the directory, and the directory's log is where a
// tenant's own administrator reads it. Whoever runs this deployment has this
// log and not that one, and until it said so a person the directory could not
// suspend, an address it could not take, and a roster this process could not
// reach each left no line here at all -- the last answered `503` to every
// call of a directory that tries again on its next cycle.
//
// So a refusal is a line: the method, the path -- which names a resource by
// identifier and nobody by anything else -- the status, and what was said.
// Louder where it is this deployment's to mend: roster not answering is a
// warning with the error it gave, which is the only place *first record does
// not look like a TLS handshake* is written down.
//
// Two answers are not refusals. `404` is what a directory is told of what is
// not there, and it asks as a matter of course -- before every create, after
// every delete. `401` is anybody's to cause on a listener a cloud reaches,
// and roster's own log has the call that was denied.
func (s *Server) failed(w http.ResponseWriter, r *http.Request, err error) {
	code, scimType, detail := refusal(err)

	switch code {
	case http.StatusNotFound:
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Bearer realm="roster"`)
	case http.StatusInternalServerError:
		s.c.Log.ErrorContext(r.Context(), "scim: unexpected", "method", r.Method, "path", r.URL.Path, "err", err)
	case http.StatusServiceUnavailable:
		s.c.Log.WarnContext(r.Context(), "scim: roster did not answer", "method", r.Method, "path", r.URL.Path, "err", err)
	default:
		at := []any{"method", r.Method, "path", r.URL.Path, "status", code}
		if scimType != "" {
			at = append(at, "scimType", scimType)
		}
		s.c.Log.InfoContext(r.Context(), "scim: refused", append(at, "detail", detail)...)
	}

	fail(w, code, scimType, detail)
}

// refusal is an error as the status, the `scimType` and the words it is
// answered with.
func refusal(err error) (int, string, string) {
	var p *problem
	if errors.As(err, &p) {
		return p.status, p.scimType, p.detail
	}

	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.InvalidArgument:
		return http.StatusBadRequest, "invalidValue", st.Message()
	case codes.AlreadyExists:
		return http.StatusConflict, "uniqueness", st.Message()
	case codes.NotFound:
		return http.StatusNotFound, "", st.Message()
	case codes.FailedPrecondition, codes.Aborted:
		return http.StatusConflict, "", st.Message()
	case codes.PermissionDenied:
		return http.StatusForbidden, "", st.Message()
	case codes.Unauthenticated:
		return http.StatusUnauthorized, "", "the key was not accepted"
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests, "", st.Message()
	case codes.Unavailable, codes.DeadlineExceeded:
		return http.StatusServiceUnavailable, "", "roster did not answer; try again"
	default:
		return http.StatusInternalServerError, "", "something went wrong here"
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
