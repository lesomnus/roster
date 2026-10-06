package scim

import (
	"encoding/json"
	"strings"
)

// patchOp is RFC 7644 §3.5.2's body, read the way the directories that send it
// write it.
//
// # Two dialects, one reading
//
// Entra wrote its PATCH its own way for years and still does for an app made
// before it changed: `op` capitalised (`Replace`, `Add`), booleans as strings
// (`"False"`), and values for several attributes in one operation with no
// path. The compliant form is lowercase, typed, and one path at a time. Both
// arrive here, so both are read: `op` without regard to case, a boolean from
// either, and an operation with no path taken as an object of attributes.
// `encoding/json` matching keys without regard to case is what lets
// `Operations` be written either way.
type patchOp struct {
	Schemas    []string `json:"schemas"`
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"Operations"`
}

// parsePatch is what a PATCH says, as [changes].
func parsePatch(body []byte) (*changes, error) {
	var p patchOp
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, badRequest("invalidSyntax", "a PatchOp, as JSON")
	}
	if len(p.Operations) == 0 {
		return nil, badRequest("invalidSyntax", "Operations: at least one")
	}

	c := &changes{}
	for _, op := range p.Operations {
		switch strings.ToLower(strings.TrimSpace(op.Op)) {
		case "add", "replace":
			if strings.TrimSpace(op.Path) != "" {
				if err := c.set(op.Path, op.Value); err != nil {
					return nil, err
				}

				continue
			}

			var all map[string]json.RawMessage
			if err := json.Unmarshal(op.Value, &all); err != nil {
				return nil, badRequest("invalidSyntax", "an operation with no path takes an object of attributes")
			}
			for k, v := range all {
				if err := c.set(k, v); err != nil {
					return nil, err
				}
			}

		case "remove":
			if strings.TrimSpace(op.Path) == "" {
				return nil, badRequest("noTarget", "remove: which attribute")
			}
			c.clear(op.Path)

		default:
			return nil, badRequest("invalidSyntax", "op: add, replace or remove")
		}
	}

	return c, nil
}
