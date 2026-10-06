package scim

import (
	"context"
	"net/http"
)

// What a client may ask the endpoint about itself (RFC 7644 §4), written down
// rather than worked out: it says what this serves, and that changes only
// when this file does.

const (
	userSchema       = "urn:ietf:params:scim:schemas:core:2.0:User"
	enterpriseSchema = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"

	configSchema       = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	resourceTypeSchema = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	schemaSchema       = "urn:ietf:params:scim:schemas:core:2.0:Schema"
)

// maxResults is the most a page of people holds.
const maxResults = 100

func serviceProviderConfig(_ context.Context, w http.ResponseWriter, _ *http.Request, _ *tenancy) error {
	answer(w, http.StatusOK, map[string]any{
		"schemas":          []string{configSchema},
		"documentationUri": "https://github.com/lesomnus/roster/blob/main/docs/scim.md",
		"patch":            map[string]any{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": maxResults},
		"changePassword":   map[string]any{"supported": false},
		"sort":             map[string]any{"supported": false},
		"etag":             map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type":        "oauthbearertoken",
			"name":        "Tenant key",
			"description": "A roster tenant key (`rt_…`), minted by `roster scim provision`.",
			"primary":     true,
		}},
		"meta": map[string]any{"resourceType": "ServiceProviderConfig", "location": Base + "/ServiceProviderConfig"},
	})

	return nil
}

func userResourceType() map[string]any {
	return map[string]any{
		"schemas":     []string{resourceTypeSchema},
		"id":          "User",
		"name":        "User",
		"endpoint":    "/Users",
		"description": "A person in this tenant.",
		"schema":      userSchema,
		"schemaExtensions": []map[string]any{
			{"schema": enterpriseSchema, "required": false},
		},
		"meta": map[string]any{"resourceType": "ResourceType", "location": Base + "/ResourceTypes/User"},
	}
}

func resourceTypes(_ context.Context, w http.ResponseWriter, _ *http.Request, _ *tenancy) error {
	answer(w, http.StatusOK, list([]any{userResourceType()}, 1, 1))

	return nil
}

func resourceType(_ context.Context, w http.ResponseWriter, r *http.Request, _ *tenancy) error {
	if r.PathValue("name") != "User" {
		return notFound("the one resource type here is User")
	}
	answer(w, http.StatusOK, userResourceType())

	return nil
}

// The attributes this reads and writes, and nothing it would not keep: a
// schema that listed `phoneNumbers` would be a directory told a value was
// stored when it was dropped.
func userSchemaDoc() map[string]any {
	str := func(name, desc string, mutability string) map[string]any {
		return map[string]any{
			"name": name, "type": "string", "multiValued": false, "description": desc,
			"required": name == "userName", "caseExact": false, "mutability": mutability,
			"returned": "default", "uniqueness": "none",
		}
	}

	return map[string]any{
		"schemas":     []string{schemaSchema},
		"id":          userSchema,
		"name":        "User",
		"description": "A person, as roster keeps one.",
		"attributes": []map[string]any{
			str("userName", "Their address, which is how a directory finds somebody here.", "immutable"),
			str("displayName", "What they are called.", "readWrite"),
			{
				"name": "name", "type": "complex", "multiValued": false, "required": false,
				"mutability": "readWrite", "returned": "default",
				"subAttributes": []map[string]any{str("formatted", "What they are called, whole.", "readWrite")},
			},
			str("preferredLanguage", "Their locale.", "readWrite"),
			{
				"name": "active", "type": "boolean", "multiValued": false, "required": false,
				"mutability": "readWrite", "returned": "default",
				"description": "Whether they may sign in, as far as the directory is concerned.",
			},
			{
				"name": "emails", "type": "complex", "multiValued": true, "required": false,
				"mutability": "immutable", "returned": "default",
				"subAttributes": []map[string]any{
					str("value", "An address.", "immutable"),
					str("type", "work", "immutable"),
					{"name": "primary", "type": "boolean", "multiValued": false, "required": false, "mutability": "immutable", "returned": "default"},
				},
			},
		},
		"meta": map[string]any{"resourceType": "Schema", "location": Base + "/Schemas/" + userSchema},
	}
}

func enterpriseSchemaDoc() map[string]any {
	return map[string]any{
		"schemas":     []string{schemaSchema},
		"id":          enterpriseSchema,
		"name":        "EnterpriseUser",
		"description": "What an organisation says about somebody, as far as roster keeps it.",
		"attributes": []map[string]any{
			{"name": "department", "type": "string", "multiValued": false, "required": false, "caseExact": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none"},
			{"name": "employeeNumber", "type": "string", "multiValued": false, "required": false, "caseExact": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none"},
		},
		"meta": map[string]any{"resourceType": "Schema", "location": Base + "/Schemas/" + enterpriseSchema},
	}
}

func schemas(_ context.Context, w http.ResponseWriter, _ *http.Request, _ *tenancy) error {
	answer(w, http.StatusOK, list([]any{userSchemaDoc(), enterpriseSchemaDoc()}, 2, 1))

	return nil
}

func schema(_ context.Context, w http.ResponseWriter, r *http.Request, _ *tenancy) error {
	switch r.PathValue("urn") {
	case userSchema:
		answer(w, http.StatusOK, userSchemaDoc())
	case enterpriseSchema:
		answer(w, http.StatusOK, enterpriseSchemaDoc())
	default:
		return notFound("no such schema here")
	}

	return nil
}

func groups(_ context.Context, w http.ResponseWriter, _ *http.Request, _ *tenancy) error {
	fail(w, http.StatusNotImplemented, "", "groups are not provisioned here yet; provision users only")

	return nil
}

// list is a ListResponse of what is already on the page.
func list(items []any, total, start int) map[string]any {
	if items == nil {
		items = []any{}
	}

	return map[string]any{
		"schemas":      []string{listSchema},
		"totalResults": total,
		"startIndex":   start,
		"itemsPerPage": len(items),
		"Resources":    items,
	}
}
