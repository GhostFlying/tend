package protocol

// WorkItemView combines the plan with current execution facts. Column is a
// projection, not an editable business stage or proof of completion.
type WorkItemView struct {
	WorkItem
	Column       string                `json:"column"`
	Tasks        []TaskSummary         `json:"tasks,omitempty"`
	Observations []DeliveryObservation `json:"observations,omitempty"`
}

type WorkItemPage struct {
	Items []WorkItemView `json:"items"`
}
