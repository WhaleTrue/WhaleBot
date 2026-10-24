package executor

import (
	"encoding/json"
	"github.com/WhaleTrue/WhaleBot/pkg/mesh"
)

func attachTools(capabilities map[string]*mesh.Capability) {
	tools := []struct{ id, name, description, path, schema string }{
		{"container.exec", "container_exec", "Execute explicit argv inside an owned Mesh Node container. No implicit shell. Output bounded to 4 MiB. A timeout or output overflow kills the container to prevent a detached process.", "/executor/exec", `{"type":"object","additionalProperties":false,"required":["id","command"],"properties":{"id":{"type":"string"},"command":{"type":"array","minItems":1,"items":{"type":"string"}},"cwd":{"type":"string"},"env":{"type":"object","additionalProperties":{"type":"string"}},"timeout_seconds":{"type":"integer","minimum":1,"maximum":7200},"async":{"type":"boolean"},"owner":{"type":"string"}}}`},
		{"container.files", "container_files", "Read, list, write, mkdir, move or delete paths inside an owned container workspace. Binary content uses base64. Root confinement rejects symlink escapes.", "/executor/files", `{"type":"object","additionalProperties":false,"required":["id","operation","path"],"properties":{"id":{"type":"string"},"operation":{"type":"string","enum":["list","read","write","mkdir","move","delete"]},"path":{"type":"string"},"to":{"type":"string"},"content_base64":{"type":"string"},"owner":{"type":"string"}}}`},
		{"artifact.export", "artifact_export", "Export an owned container workspace path as a persistent authenticated tar.gz attachment. Rejects symlinks and special files. Download expires after 24 hours.", "/executor/artifacts/export", `{"type":"object","additionalProperties":false,"required":["id","path"],"properties":{"id":{"type":"string"},"path":{"type":"string"},"owner":{"type":"string"}}}`},
	}
	for _, t := range tools {
		c := capabilities[t.id]
		schema := json.RawMessage(t.schema)
		c.InputSchema = schema
		c.Tool = &mesh.ToolExport{Name: t.name, Description: t.description, InputSchema: schema, Method: "POST", Path: t.path, Scopes: []string{t.id}, Confirmation: true}
	}
	// These schemas describe each capability's primary endpoint. Secondary
	// operations retain their explicit method/path contracts.
	for id, schema := range map[string]string{
		"container.manage":     `{"type":"array","items":{"type":"object","required":["Id","Names","Image","State","Labels","Config"],"properties":{"Id":{"type":"string"},"Names":{"type":["array","null"],"items":{"type":"string"}},"Image":{"type":"string"},"Labels":{"type":["object","null"],"additionalProperties":{"type":"string"}},"Config":{"type":"object"}}}}`,
		"container.exec":       `{"type":"object","additionalProperties":false,"required":["id","principal","kind","state","started_at","exit_code"],"properties":{"id":{"type":"string"},"principal":{"type":"string"},"kind":{"enum":["exec","pull"]},"container_id":{"type":"string"},"engine_id":{"type":"string"},"state":{"enum":["queued","running","complete","failed","interrupted"]},"started_at":{"type":"string"},"finished_at":{"type":"string"},"stdout":{"type":"string"},"stderr":{"type":"string"},"exit_code":{"type":"integer"},"error":{"type":"string"}}}`,
		"container.files":      `{"type":"object","additionalProperties":false,"required":["path","size"],"properties":{"path":{"type":"string"},"size":{"type":"integer","minimum":0},"content_base64":{"type":"string","contentEncoding":"base64"},"entries":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["name","directory","size","modified_at"],"properties":{"name":{"type":"string"},"directory":{"type":"boolean"},"size":{"type":"integer"},"modified_at":{"type":"string"}}}},"error":{"type":"string"}}}`,
		"artifact.export":      `{"type":"object","additionalProperties":false,"required":["id","name","url","media_type","owner","created_at","expires_at"],"properties":{"id":{"type":"string"},"name":{"type":"string"},"url":{"type":"string"},"media_type":{"const":"application/gzip"},"owner":{"type":"string"},"created_at":{"type":"string"},"expires_at":{"type":"string"}}}`,
		"container.images":     `{"type":"object","additionalProperties":false,"required":["images","node_image","allowed_images"],"properties":{"images":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["Id","RepoTags","RepoDigests","Size"],"properties":{"Id":{"type":"string"},"RepoTags":{"type":["array","null"],"items":{"type":"string"}},"RepoDigests":{"type":["array","null"],"items":{"type":"string"}},"Size":{"type":"integer","minimum":0}}}},"node_image":{"type":"string"},"allowed_images":{"type":["array","null"],"items":{"type":"string"}}}}`,
		"deployment.reconcile": `{"type":"array","items":{"type":"object","additionalProperties":false,"required":["deployment","principal","updated_at"],"properties":{"deployment":{"type":"object","required":["schema","id","namespace","node","image"],"properties":{"schema":{"const":"whalemesh.deployment/v1"},"id":{"type":"string"},"namespace":{"type":"string"},"node":{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string"},"labels":{"type":["object","null"],"additionalProperties":{"type":"string"}},"annotations":{"type":["object","null"],"additionalProperties":{"type":"string"}},"requires":{"type":["array","null"],"items":{"type":"object"}}}},"requires":{"type":["array","null"],"items":{"type":"object"}},"image":{"type":"string"}}},"principal":{"type":"string"},"updated_at":{"type":"string"},"error":{"type":"string"}}}}`,
	} {
		capabilities[id].OutputSchema = json.RawMessage(schema)
	}
	for _, id := range []string{"container.manage", "deployment.reconcile"} {
		capabilities[id].InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"owner":{"type":"string"}}}`)
	}
	capabilities["container.images"].InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"owner":{"type":"string"},"approval_ids":{"type":"string"}}}`)
}
