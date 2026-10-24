package providers

import "github.com/WhaleTrue/WhaleBot/pkg/mesh"

func describe(capability *mesh.Capability) {
	var input, output any
	switch capability.ID {
	case "session.store":
		input, output = SessionRequest{}, Session{}
	case "memory.store":
		input, output = KeyRequest{}, Note{}
	case "memory.search":
		input, output = SearchRequest{}, struct {
			Items []Note `json:"items"`
		}{}
	case "secrets.resolve":
		input, output = KeyRequest{}, Secret{}
	case "skills.search":
		input, output = SearchRequest{}, struct {
			Items []Skill `json:"items"`
		}{}
	case "workspace.files":
		input, output = WorkspaceInput{}, WorkspaceFile{}
	default:
		input, output = EventsRequest{}, struct {
			Written int `json:"written"`
		}{}
	}
	capability.InputSchema = mesh.JSONSchemaFor(input)
	capability.OutputSchema = mesh.JSONSchemaFor(output)
	capability.Semantics = capability.Operations[0].Semantics
	switch capability.ID {
	case "memory.search", "skills.search":
		name, description := "memory_search", "Search your durable notes by case-insensitive text."
		if capability.ID == "skills.search" {
			name, description = "skills_search", "Search your versioned skill packages and retrieve matching package files."
		}
		capability.Tool = &mesh.ToolExport{Name: name, Description: description, InputSchema: capability.InputSchema, Method: "POST", Path: capability.Endpoint, Scopes: []string{capability.ID}}
	case "memory.store":
		capability.Tool = &mesh.ToolExport{Name: "memory_put", Description: "Save a durable note under a key; replace the previous value if that key exists.", InputSchema: mesh.JSONSchemaFor(NoteInput{}), Method: "PUT", Path: "/memory/put", Scopes: []string{"memory.write"}, Confirmation: true}
	}
}
