package cmd_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"

	"github.com/lesomnus/roster/cmd"
)

// clockLeaf is a duration or a count of a file, by its key, where it is.
type clockLeaf struct {
	key string
	v   reflect.Value
}

// clocksOf is every duration and every signed count `v` holds, by the key the
// file writes it with, as the loader walks it: a struct's fields by their
// `yaml` names, an inline one in its parent, a map of blocks by each name it
// holds.
func clocksOf(v reflect.Value, at string) []clockLeaf {
	out := []clockLeaf{}
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "-" {
				continue
			}
			key := at
			if opts != "inline" {
				if name == "" {
					name = strings.ToLower(f.Name)
				}
				key = strings.TrimPrefix(at+"."+name, ".")
			}
			out = append(out, clocksOf(v.Field(i), key)...)
		}

	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			for _, l := range clocksOf(e, at+"."+k.String()) {
				out = append(out, clockLeaf{key: l.key, v: reflect.Value{}})
			}
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
		out = append(out, clockLeaf{key: at, v: v})
	}

	return out
}

// clockLeftOut is the durations and counts of the file a negative is not
// refused for, and why.
var clockLeftOut = map[string]string{
	"server.timeout":  "payday reads it, and gives less than none a meaning of its own: no deadline at all",
	"control.timeout": "the same",
	"admin.timeout":   "the same",
}

// TestAClockLessThanNoneIsRefused: #398 refused a negative duration under
// `audit:` and #429 a negative `scim.concurrency`, each because it is read as
// if nothing were written; every other clock and count roster reads took one
// the same way. `holder.forget_after: -720h` was never, `consent.keep`
// negative for good, `stream.recheck` negative five minutes, and a negative
// `server.keepalive.time` gave back gRPC's two hours. Each is refused, naming
// the key.
//
// The keys are found by walking the configuration, so that one added is held
// too; those left out are named in [clockLeftOut], with why. `otel:` is not
// walked: what reads it is the telemetry library, not roster.
func TestAClockLessThanNoneIsRefused(t *testing.T) {
	// A kind's block, so that a map of blocks is walked too.
	shape := cmd.Config{Audit: config.AuditConfig{By: map[string]config.AuditKeepConfig{"holder": {}}}}
	shape.Otel = config.OtelConfig{}

	keys := []string{}
	for _, l := range clocksOf(reflect.ValueOf(&shape).Elem(), "") {
		if strings.HasPrefix(l.key, "otel.") {
			continue
		}
		keys = append(keys, l.key)
	}
	slices.Sort(keys)
	for _, want := range []string{
		"holder.forget_after", "holder.every", "consent.keep", "consent.every",
		"tenant.restore_within", "tenant.alias_hold", "tenant.every",
		"stream.recheck", "watch.drain_every", "control.watch.drain_every",
		"vouch.lockout.for", "vouch.lockout.failures", "vouch.password.min_length",
		"account.mail.timeout", "policy.refresh", "scim.concurrency",
		"server.keepalive.time", "control.keepalive.min_time", "admin.keepalive.timeout",
	} {
		require.Contains(t, keys, want, "the walk finds what the reader listed")
	}

	for _, key := range keys {
		if why, ok := clockLeftOut[key]; ok {
			t.Logf("%s: left out: %s", key, why)

			continue
		}

		t.Run(key, func(t *testing.T) {
			err := startedWith(t, func(c *cmd.Config) {
				c.Audit.By = map[string]config.AuditKeepConfig{"holder": {Profile: "forever"}}
				if strings.HasPrefix(key, "audit.by.holder.") {
					// A map's value cannot be set where it is.
					v := c.Audit.By["holder"]
					setClock(t, reflect.ValueOf(&v).Elem(), strings.TrimPrefix(key, "audit.by.holder."))
					v.Profile = ""
					c.Audit.By["holder"] = v

					return
				}
				setClock(t, reflect.ValueOf(c).Elem(), key)
			})
			require.Error(t, err, "less than none")
			require.Contains(t, err.Error(), key+":")
		})
	}

	t.Run("nought is what each key's comment says", func(t *testing.T) {
		require.NoError(t, startedWith(t, nil))
	})
}

// setClock sets the duration or count at `key` under `v` to less than none.
func setClock(t *testing.T, v reflect.Value, key string) {
	t.Helper()

	for _, l := range clocksOf(v, "") {
		if l.key != key {
			continue
		}
		switch l.v.Kind() {
		case reflect.Float32, reflect.Float64:
			l.v.SetFloat(-1)
		default:
			if l.v.Type() == reflect.TypeFor[time.Duration]() {
				l.v.SetInt(int64(-time.Hour))
			} else {
				l.v.SetInt(-1)
			}
		}

		return
	}
	t.Fatalf("%s: not found", key)
}
