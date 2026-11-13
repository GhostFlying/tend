package harness

import "encoding/json"

const UploadFileTool = "upload_file"

// UploadFileToolSpec submits real local bytes under the actual execution's
// credential. The returned metadata is used by the existing result interface.
var UploadFileToolSpec = ToolSpec{Name: UploadFileTool, Description: "Upload a real local output file under this execution's own identity. Publish its file_id directly with ordinary conversation text/attachments when visible in the current responsibility; no artifact, acceptance or delivery registration is required for messaging. Use the returned immutable metadata in issues_update_delivery action=submit_result only when freezing a formal result for review or settlement. Preserve the original path/content and name after uncertain receipt loss. Upload alone does not publish a result or grant access to another Issue.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"name":{"type":"string"}},"required":["path","name"],"additionalProperties":false}`)}
