package protocol

import "reflect"

// ValidateAgentInventoryUpdate keeps current definition and binding revisions
// meaningful. Historical inventories and exact request replay remain unchanged.
func ValidateAgentInventoryUpdate(next, previous AgentInventoryProjection) error {
	if next.FormatVersion < 3 && previous.FormatVersion < 3 {
		return nil
	}
	if previous.FormatVersion >= 3 && next.FormatVersion < previous.FormatVersion {
		return Errorf(CodeInvalidRequest, "single-entry inventory cannot downgrade its format")
	}
	for _, n := range next.Agents {
		for _, old := range previous.Agents {
			if n.Identity == old.Identity && (n.Revision < old.Revision || n.Revision == old.Revision && !reflect.DeepEqual(n, old)) {
				return Errorf(CodeInvalidRequest, "changed Agent definition requires a new revision")
			}
		}
	}
	for _, n := range next.Runtimes {
		for _, old := range previous.Runtimes {
			if n.RuntimeID == old.RuntimeID && (n.Revision < old.Revision || n.Revision == old.Revision && !reflect.DeepEqual(n, old)) {
				return Errorf(CodeInvalidRequest, "changed entry binding requires a new revision")
			}
		}
	}
	return nil
}
