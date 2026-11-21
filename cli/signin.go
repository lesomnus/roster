package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/roster/cmd"
)

// NewCmdSignIn is `roster sign-in`: a key for this terminal, without a clipboard.
//
// It prints a URL and eight characters, and waits. Somebody opens the URL in a
// browser they are already signed in at, types the characters, sees what the
// terminal asked to be allowed, and says yes; this then writes the key it is
// handed. RFC 8628, the device grant, which is the shape every CLI with a *go
// here and type this* screen uses -- and it is polling rather than anything
// cleverer, because the machine running this may have no browser and nothing
// that can be called back.
//
// # It talks to the account app, not to roster
//
// Which is the whole of the design and is `account/device.go`'s argument: a
// terminal holds no credential, roster answers nothing without one, and making it
// answer *something* without one would be an unmetered guessing surface with no
// frame to count against (`cmd/serve.go`, `Public`). The front door already holds
// a key, already knows which operator a host belongs to, and already mints an
// `rt_` as the person -- so it does the flow and roster does not change.
//
// So `--at` is an account app's base URL, and there is no `client.addr` in this
// command at all. What comes back is an ordinary tenant key: it belongs to the
// person who approved it, is never wider than they are, and is revoked from the
// same page as an app password.
func NewCmdSignIn(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "sign-in",
		Brief: "a key for this terminal, approved in a browser",

		Flags: flg.Flags{
			&flg.String{
				Name:  "at",
				Brief: "the account app to sign in at, e.g. https://account.contoso.example",
			},
			&flg.String{
				Name:  "name",
				Brief: "what to call this terminal, unique among your keys",
			},
			&flg.Strings{
				Name:  "allow",
				Brief: "the methods it may call; repeat it, or comma separate",
			},
			&flg.String{
				Name:  "out",
				Brief: "write the key here instead of printing it",
			},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			at, _ := flg.Find[string](cl, "at")
			if at == "" {
				return errors.New("--at: which account app to sign in at")
			}
			name, _ := flg.Find[string](cl, "name")
			if name == "" {
				name, _ = os.Hostname()
			}

			raw, _ := flg.Find[[]string](cl, "allow")
			allow := cmd.SplitMethods(raw)
			// Refused rather than defaulted, which is `roster key add`'s decision:
			// a credential nobody chose the reach of is either wider than somebody
			// meant or narrower than works.
			if len(allow) == 0 {
				return errors.New("--allow: what this terminal may call, e.g. /roster.MeService/Get")
			}

			// Over `https`, since what comes back is a key; `http` is taken
			// for this machine alone, which is a development rig.
			u, err := url.Parse(at)
			if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && thisMachine(u.Hostname()))) {
				return fmt.Errorf("--at %s: an https address of the account app; http is taken for this machine alone", at)
			}

			// A client that gives up on one request, which the default never
			// does: a front door that took a connection and said nothing held
			// this command for as long as it liked.
			//
			// What is said to the person -- where to go, what to type, that it
			// is waiting -- on standard error, and the key alone on standard
			// output. All of it was on standard output, so `roster sign-in …
			// > key` put the code to type in the file and nothing on the
			// terminal, and nobody could approve it (#284).
			say := func(format string, vs ...any) (int, error) { return fmt.Fprintf(cl.ErrWriter, format, vs...) }
			key, err := SignIn(ctx, say, &http.Client{Timeout: 30 * time.Second}, strings.TrimSuffix(at, "/"), name, allow)
			if err != nil {
				return err
			}

			out, _ := flg.Find[string](cl, "out")
			if out == "" {
				// stdout, so `$(roster sign-in …)` is the key and nothing else --
				// the shape `roster key add` answers in, and for its reason.
				fmt.Fprintf(os.Stdout, "%s\n", key)
				say("this is the only time it is shown. `client.auth.credential_file` is where a configuration reads one.\n")

				return nil
			}

			if err := keep(out, key); err != nil {
				return err
			}
			say("written to %s. point `client.auth.credential_file` at it.\n", out)

			return nil
		}),
	}
}

// keep writes a key a terminal was handed into the file `--out` names.
//
// `0600` in a directory made at `0700`: it is a credential, and the file is the
// only thing keeping it. Through [writeKey] -- written beside and moved into
// place -- because that is what makes the mode true. It was `os.WriteFile`
// with `0600`, which is the mode of a file it **creates**: one that is already
// there keeps its own, and the file a second sign-in writes to is the one the
// first left, or one somebody made by hand, readable by whoever it was
// readable by (#276).
func keep(out, key string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}

	return writeKey(out, key)
}

// begun is what the front door answers when a flow starts: RFC 8628 §3.2.
type begun struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	At         string `json:"verification_uri"`
	Complete   string `json:"verification_uri_complete"`
	ExpiresIn  int    `json:"expires_in"`
	Interval   int    `json:"interval"`
}

// SignIn walks the flow: begin, say where to go, and poll until somebody has
// answered or the code has run out.
//
// Exported, and taking both the printer and the client, for one reason: this and
// `account/device.go` were written from the same document, which is exactly when
// two halves drift -- one of them renames `user_code` or stops sending
// `authorization_pending` and nothing notices. `account/device_test.go` drives
// this against the real front door, so the two are held together by a test rather
// than by both having been read once.
func SignIn(ctx context.Context, say func(string, ...any) (int, error), cl *http.Client, at, name string, allow []string) (string, error) {
	body, err := json.Marshal(map[string]any{"name": name, "allow": allow})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, at+"/device/begin", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")

	res, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", at, strings.TrimSpace(read(res)))
	}

	var v begun
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}

	// The code first and on its own line, because it is what somebody is about to
	// type, and the complete URL after it for anybody whose terminal makes a link
	// clickable -- RFC 8628 §3.3.1's `verification_uri_complete`, which is the
	// same thing a QR code carries.
	_, _ = say("\n  open      %s\n  and type  %s\n\n", v.At, v.UserCode)
	if v.Complete != "" {
		_, _ = say("  or open   %s\n\n", v.Complete)
	}
	_, _ = say("waiting, for up to %s...\n", (time.Duration(v.ExpiresIn) * time.Second).String())

	wait := time.Duration(max(v.Interval, 1)) * time.Second

	// The code's own clock, kept here too: a front door that cannot be reached
	// reads as *not yet* below, so that a moment's outage is not the end of
	// the flow, and without this it read as *not yet* for ever.
	ctx, cancel := context.WithTimeoutCause(ctx, time.Duration(max(v.ExpiresIn, 1))*time.Second+wait,
		errors.New("the code ran out before anybody approved it"))
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return "", context.Cause(ctx)
		case <-time.After(wait):
		}

		token, why, err := poll(ctx, cl, at, v.DeviceCode)
		if err != nil {
			return "", err
		}
		switch why {
		case "":
			return token, nil
		case "authorization_pending":
			// keep going
		case "slow_down":
			// §3.5: add five seconds and carry on, rather than giving up. A
			// client that ignores this is one the front door stops answering.
			wait += 5 * time.Second
		case "access_denied":
			return "", errors.New("somebody said no")
		case "expired_token":
			return "", errors.New("the code ran out before anybody approved it")
		default:
			return "", fmt.Errorf("%s: %s", at, why)
		}
	}
}

// poll asks once. It answers the key, or the reason to keep waiting.
func poll(ctx context.Context, cl *http.Client, at, code string) (string, string, error) {
	// Posted, with the code in the body: it is the whole of what a poll is held
	// to, and an address is what every log between here and there keeps
	// (`account/device.go`, `devicePoll`).
	body, err := json.Marshal(map[string]string{"device_code": code})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, at+"/device/poll", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("content-type", "application/json")

	res, err := cl.Do(req)
	if err != nil {
		// A front door that is briefly unreachable is not a flow that is over:
		// the code is good until it expires, so this is a reason to wait rather
		// than to stop.
		return "", "authorization_pending", nil
	}
	defer res.Body.Close()

	var out struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("%s: %w", at, err)
	}
	if out.Token != "" {
		return out.Token, "", nil
	}
	if out.Error == "" {
		return "", "", fmt.Errorf("%s: an answer that is neither a key nor a reason", at)
	}

	return "", out.Error, nil
}

// thisMachine is whether a host is this machine's own name for itself.
func thisMachine(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func read(res *http.Response) string {
	var b bytes.Buffer
	_, _ = b.ReadFrom(res.Body)

	return b.String()
}
