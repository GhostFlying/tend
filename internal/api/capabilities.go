package api

import (
	"net/http"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type ownerRoute struct {
	pattern string
	handler http.HandlerFunc
}

// ownerRoutes is the actual registration table, also used by the CLI contract check.
func (s *Server) ownerRoutes() []ownerRoute {
	return []ownerRoute{
		{"GET /api/v1/diagnostics/{kind}", s.triggerList},
		{"GET /api/v1/diagnostics/{kind}/{id}", s.triggerDetail},
		{"GET /api/v1/diagnostics/events/{id}/sources", s.triggerSources},
		{"PUT /api/v1/webhook-keys/{id}", s.stageWebhookKey},
		{"GET /api/v1/messaging/channels", s.messaging},
		{"GET /api/v1/messaging/channels/{channel}", s.messaging},
		{"GET /api/v1/messaging/messages", s.messaging},
		{"GET /api/v1/messaging/messages/{id}", s.messaging},
		{"GET /api/v1/messaging/messages/{id}/{view}", s.messaging},
		{"GET /api/v1/messaging/events", s.events},
		{"GET /api/v1/archives", s.archiveCatalog},
		{"GET /api/v1/archives/{archive}", s.archiveDetail},
		{"GET /api/v1/archives/{archive}/records/{kind}", s.archiveRecords},
		{"GET /api/v1/archives/{archive}/records/{kind}/{id}", s.archiveRecord},
		{"GET /api/v1/archives/{archive}/originals/{kind}", s.archiveOriginals},
		{"GET /api/v1/archives/{archive}/originals/{kind}/{id}/metadata", s.archiveOriginalMetadata},
		{"GET /api/v1/archives/{archive}/originals/{kind}/{id}/download-url", s.archiveOriginalURL},
		{"GET /api/v1/archives/{archive}/originals/{kind}/{id}", s.archiveOriginal},
		{"GET /api/v1/archives/{archive}/resources", s.archiveResources},
		{"GET /api/v1/archives/{archive}/resources/{id}/files/{index}/download-url", s.archiveResourceURL},
		{"GET /api/v1/archives/{archive}/resources/{id}/files/{index}", s.archiveResourceFile},
		{"GET /api/v1/deployment", s.deployment},
		{"POST /api/v1/session", s.createSession},
		{"GET /api/v1/session", s.readSession},
		{"POST /api/v1/ops", s.ops},
		{"GET /api/v1/snapshot", s.snapshot},
		{"GET /api/v1/upgrades/{id}", s.upgrade},
		{"GET /api/v1/categories/{id}", s.agentCategory},
		{"GET /api/v1/categories", s.agentCategories},
		{"GET /api/v1/hosts/{id}/agents", s.runnerAgentCatalog},
		{"GET /api/v1/repos", s.repos},
		{"GET /api/v1/repos/{id}", s.repository},
		{"GET /api/v1/sources/{id}", s.manualSource},
		{"GET /api/v1/runs/{id}/messages", s.runMessages},
		{"GET /api/v1/runs/{id}/native-entry", s.nativeEntry},
		{"GET /api/v1/tasks", s.tasks},
		{"GET /api/v1/tasks/{id}", s.task},
		{"GET /api/v1/work-item-choices/{id}", s.workItemChoice},
		{"GET /api/v1/background/{id}/sources", s.backgroundSources},
		{"GET /api/v1/coordination/metrics", s.coordinationMetrics},
		{"GET /api/v1/token-usage", s.tokenUsage},
		{"GET /api/v1/hosted-agents", s.hostedAgents},
		{"GET /api/v1/deadlines", s.deadlines},
		{"GET /api/v1/deadlines/{id}", s.deadline},
		{"GET /api/v1/next-actions", s.nextActions},
		{"GET /api/v1/planner-entries/{id}", s.plannerEntry},
		{"GET /api/v1/attentions", s.attentions},
		{"GET /api/v1/attentions/{id}", s.attention},
		{"GET /api/v1/inbox", s.attentions},
		{"GET /api/v1/issues", s.issues},
		{"GET /api/v1/issue-notification-settings", s.issueNotificationSettings},
		{"GET /api/v1/issues/{id}", s.issue},
		{"GET /api/v1/issues/{id}/activity", s.issueActivity},
		{"GET /api/v1/issues/{id}/results", s.issueResults},
		{"GET /api/v1/issues/{id}/context", s.issueContext},
		{"GET /api/v1/issues/{id}/followups", s.issueFollowups},
		{"GET /api/v1/issues/{id}/followups/{rule}/history", s.issueFollowupHistory},
		{"GET /api/v1/issues/{id}/deliveries", s.issueDeliveries},
		{"GET /api/v1/files/{id}", s.downloadFile},
		{"GET /api/v1/files/{id}/metadata", s.fileMetadata},
		{"GET /api/v1/runs/{id}/logs", s.runLogs},
		{"GET /api/v1/runs/{id}/rollout", s.runRollout},
		{"GET /api/v1/requests/{id}", s.request},
		{"GET /api/v1/conversation", s.conversation},
		{"GET /api/v1/agent-conversations", s.agentConversations},
		{"GET /api/v1/agent-conversations/{id}", s.agentConversation},
		{"GET /api/v1/messages/{id}/associations", s.messageAssociations},
		{"GET /api/v1/messages/{id}", s.ownerMessage},
		{"GET /api/v1/agent-conversations/{id}/rollout", s.agentRollout},
		{"GET /api/v1/agent-conversations/{id}/rollout/{seq}", s.agentRolloutWindow},
		{"GET /api/v1/agent-conversations/{id}/usage", s.agentUsage},
		{"GET /api/v1/agent-conversations/{id}/compactions", s.agentCompactions},
		{"GET /api/v1/conversation/queue", s.conversationQueue},
		{"GET /api/v1/conversation/transcript", s.conversationTranscript},
		{"GET /api/v1/conversation/blocks/{id}", s.conversationBlock},
		{"GET /api/v1/conversation/images/{id}", s.conversationImage},
		{"POST /api/v1/conversation/attachments", s.uploadAttachment},
		{"GET /api/v1/conversation/attachments/{id}", s.downloadAttachment},
		{"GET /api/v1/conversation/attachments/{id}/metadata", s.attachmentMetadata},
		{"GET /api/v1/conversation/attachments/{id}/download-url", s.attachmentDownloadURL},
		{"GET /api/v1/conversation/images/{id}/content", s.downloadAttachment},
		{"GET /api/v1/conversation/images/{id}/download-url", s.attachmentDownloadURL},
		{"GET /api/v1/files/{id}/download-url", s.deliverableDownloadURL},
		{"GET /api/v1/conversation/settings", s.conversationSettings},
		{"GET /api/v1/conversation/handoff", s.conversationHandoff},
		{"GET /api/v1/events", s.events},
		{"GET /api/v1/artifacts/{id}", s.artifact},
	}
}

// OwnerCapabilities enumerates the actual owner interfaces, including gated operations.
// Authentication and authorization remain in the existing handlers and application commands.
func OwnerCapabilities() []string {
	var out []string
	for _, route := range (*Server)(nil).ownerRoutes() {
		out = append(out, route.pattern)
	}
	for operation := range ownerCommands {
		out = append(out, operation)
	}
	for operation := range hostedCommands {
		out = append(out, operation)
	}
	out = append(out, "conversation.set_settings")
	slices.Sort(out)
	return out
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Artifact(r.Context(), q, r.PathValue("id"))
	})
}
