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

func groupResourceType() map[string]any {
	return map[string]any{
		"schemas":     []string{resourceTypeSchema},
		"id":          "Group",
		"name":        "Group",
		"endpoint":    "/Groups",
		"description": "A group of this tenant that its directory made.",
		"schema":      groupSchema,
		"meta":        map[string]any{"resourceType": "ResourceType", "location": Base + "/ResourceTypes/Group"},
	}
}

func resourceTypes(_ context.Context, w http.ResponseWriter, _ *http.Request, _ *tenancy) error {
	answer(w, http.StatusOK, list([]any{userResourceType(), groupResourceType()}, 2, 1))

	return nil
}

func resourceType(_ context.Context, w http.ResponseWriter, r *http.Request, _ *tenancy) error {
	switch r.PathValue("name") {
	case "User":
		answer(w, http.StatusOK, userResourceType())
	case "Group":
		answer(w, http.StatusOK, groupResourceType())
	default:
		return notFound("the resource types here are User and Group")
	}

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
			// `name` is not listed, and is read: a `name.formatted` is what
			// somebody is called where a directory says no `displayName`, and
			// not a second name kept beside it -- so a schema that listed it
			// would be a directory told two values were stored when one was.
			str("displayName", "What they are called.", "readWrite"),
			str("preferredLanguage", "Their locale.", "readWrite"),
			{
				"name": "phoneNumbers", "type": "complex", "multiValued": true, "required": false,
				"mutability": "readWrite", "returned": "default",
				"description": "Where they are reached: one number a kind.",
				"subAttributes": []map[string]any{
					str("value", "The number, as it was said.", "readWrite"),
					{
						"name": "type", "type": "string", "multiValued": false, "required": false, "caseExact": false,
						"mutability": "readWrite", "returned": "default", "uniqueness": "none",
						"canonicalValues": phoneKinds, "description": "Which of their numbers it is.",
					},
				},
			},
			{
				"name": "active", "type": "boolean", "multiValued": false, "required": false,
				"mutability": "readWrite", "returned": "default",
				"description": "Whether they may sign in, as far as the directory is concerned.",
			},
			{
				"name": "emails", "type": "complex", "multiValued": true, "required": false,
				"mutability": "immutable", "returned": "default",
				// Of one type, and said so. A directory writes and reads an
				// address by its type -- `emails[type eq "work"].value` -- and
				// Microsoft's validator makes one up for each type a schema
				// names: named none, it sent an address of no type and looked
				// for one of no type, which the `work` this answers was not.
				//
				// `primary` is answered and not listed, as `name` is read and
				// not listed: it is no value kept. The address a directory
				// says is the primary one, and there is no second for it to
				// say which of.
				"subAttributes": []map[string]any{
					str("value", "An address.", "immutable"),
					{
						"name": "type", "type": "string", "multiValued": false, "required": false, "caseExact": false,
						"mutability": "immutable", "returned": "default", "uniqueness": "none",
						"canonicalValues": []string{"work"}, "description": "Which of their addresses it is: the one a directory says is their `work` one.",
					},
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
			{
				"name": "manager", "type": "complex", "multiValued": false, "required": false,
				"mutability": "readWrite", "returned": "default",
				"description": "Who they report to: a User here.",
				"subAttributes": []map[string]any{
					{"name": "value", "type": "string", "multiValued": false, "required": false, "caseExact": true, "mutability": "readWrite", "returned": "default", "uniqueness": "none", "description": "The manager's id."},
					{"name": "$ref", "type": "reference", "referenceTypes": []string{"User"}, "multiValued": false, "required": false, "mutability": "readOnly", "returned": "default"},
				},
			},
		},
		"meta": map[string]any{"resourceType": "Schema", "location": Base + "/Schemas/" + enterpriseSchema},
	}
}

// What a group is here: what it is called, and who is in it. `externalId` is
// common to every resource and is not a schema's to list.
func groupSchemaDoc() map[string]any {
	return map[string]any{
		"schemas":     []string{schemaSchema},
		"id":          groupSchema,
		"name":        "Group",
		"description": "A group, as roster keeps one a directory made.",
		"attributes": []map[string]any{
			{
				"name": "displayName", "type": "string", "multiValued": false, "required": true, "caseExact": false,
				"mutability": "readWrite", "returned": "default", "uniqueness": "server",
				"description": "What the group is called.",
			},
			{
				"name": "members", "type": "complex", "multiValued": true, "required": false,
				"mutability": "readWrite", "returned": "default",
				"description": "Who is in it: the people this endpoint speaks of.",
				"subAttributes": []map[string]any{
					{"name": "value", "type": "string", "multiValued": false, "required": false, "caseExact": true, "mutability": "immutable", "returned": "default", "uniqueness": "none", "description": "A User's id."},
					{"name": "$ref", "type": "reference", "referenceTypes": []string{"User"}, "multiValued": false, "required": false, "mutability": "immutable", "returned": "default"},
					{"name": "type", "type": "string", "multiValued": false, "required": false, "caseExact": false, "mutability": "immutable", "returned": "default", "uniqueness": "none", "description": "User."},
				},
			},
		},
		"meta": map[string]any{"resourceType": "Schema", "location": Base + "/Schemas/" + groupSchema},
	}
}

func schemas(_ context.Context, w http.ResponseWriter, _ *http.Request, _ *tenancy) error {
	answer(w, http.StatusOK, list([]any{userSchemaDoc(), enterpriseSchemaDoc(), groupSchemaDoc()}, 3, 1))

	return nil
}

func schema(_ context.Context, w http.ResponseWriter, r *http.Request, _ *tenancy) error {
	switch r.PathValue("urn") {
	case userSchema:
		answer(w, http.StatusOK, userSchemaDoc())
	case enterpriseSchema:
		answer(w, http.StatusOK, enterpriseSchemaDoc())
	case groupSchema:
		answer(w, http.StatusOK, groupSchemaDoc())
	default:
		return notFound("no such schema here")
	}

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
