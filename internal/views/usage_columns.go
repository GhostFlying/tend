package views

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// UsageDimensionFields is the shared typed dimension, stored once rather than
// embedding the same attribution, Issue titles and model in every response.
const UsageDimensionFields = `subject,role,binding_run,binding_conversation,session_id,parent_session_id,model,provider,native_version,native,execution_state,issue_path,task_id,task_title,run_id,agent,agent_id,categories`

func usageDimensionColumns(alias string) string {
	return alias + "." + strings.ReplaceAll(UsageDimensionFields, ",", ","+alias+".")
}

type usageJSONColumn struct{ value any }

func (c usageJSONColumn) Scan(value any) error {
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("invalid usage dimension column %T", value)
	}
	return json.Unmarshal([]byte(s), c.value)
}

func usageDimensionDestinations(d *UsageDimension, native *bool) []any {
	r, a := &d.Record, &d.Record.Attribution
	return []any{&r.Subject, &r.Role, &d.BindingRun, &d.BindingConversation, &r.SessionID, &r.ParentSessionID, &r.Model, &r.Provider, &r.NativeVersion, native, &a.ExecutionState, usageJSONColumn{&a.IssuePath}, &a.TaskID, &a.TaskTitle, &a.RunID, &a.Agent, &a.AgentID, usageJSONColumn{&a.Categories}}
}

const usageTotalsFields = `requests,final,input,input_known,cache_read,cache_read_known,cache_write,cache_write_known,output,output_known,total,total_known,estimated_usd,estimated_requests`

func usageTotalsColumns(alias string) string {
	return alias + "." + strings.ReplaceAll(usageTotalsFields, ",", ","+alias+".")
}

func usageTotalsDestinations(t *protocol.TokenUsageTotals) []any {
	return []any{&t.Requests, &t.Final, &t.Input, &t.InputKnown, &t.CacheRead, &t.CacheReadKnown, &t.CacheWrite, &t.CacheWriteKnown, &t.Output, &t.OutputKnown, &t.Total, &t.TotalKnown, &t.EstimatedUSD, &t.EstimatedRequests}
}
