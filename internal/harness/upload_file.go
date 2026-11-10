package harness

import "encoding/json"

const UploadFileTool = "upload_file"

// UploadFileToolSpec submits real local bytes under the actual execution's
// credential. The returned metadata is used by the existing result interface.
var UploadFileToolSpec = ToolSpec{Name: UploadFileTool, Description: "Upload a real local output file under this execution's own identity. Return the resulting immutable file metadata in issues_update_delivery action=submit_result; no worker Task is required. Preserve the original path/content and name after uncertain receipt loss. Upload alone does not publish a result or grant access to another Issue.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"name":{"type":"string"}},"required":["path","name"],"additionalProperties":false}`)}
