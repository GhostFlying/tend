package protocol

// TaskRelationKind distinguishes follow-up provenance from a general reference.
type TaskRelationKind string

const (
	RelationFollowUp TaskRelationKind = "follow_up"
	RelationRelated  TaskRelationKind = "related"
)

// RelationDirection identifies which task declared the reference.
type RelationDirection string

const (
	RelationOutgoing RelationDirection = "outgoing"
	RelationIncoming RelationDirection = "incoming"
)

// TaskRelation is a reference declared by a task to another task. Relations
// are task metadata, not scheduling dependencies or workspace inheritance.
type TaskRelation struct {
	Kind   TaskRelationKind `json:"kind"`
	TaskID string           `json:"task_id"`
}

// TaskRelationView shows a reference and the other task. Outgoing means this
// task declared it; incoming means the other task declared it.
type TaskRelationView struct {
	Kind      TaskRelationKind  `json:"kind"`
	Direction RelationDirection `json:"direction"`
	Task      TaskSummary       `json:"task"`
}
