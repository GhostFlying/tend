package agentconfig

import (
	"reflect"
	"slices"
)

// ValidateUpdate protects new single-entry definition revisions without
// rewriting historical formats or changing the identity of retained snapshots.
func ValidateUpdate(next, previous Config) error {
	if next.FormatVersion < 3 && previous.FormatVersion < 3 {
		return nil
	}
	if previous.FormatVersion >= 3 && next.FormatVersion < previous.FormatVersion {
		return invalid("format_downgrade", "$.format_version")
	}
	normalize := func(c Config) Config {
		c = cloneConfig(c)
		for i := range c.Agents {
			slices.Sort(c.Agents[i].Categories)
			slices.Sort(c.Agents[i].Purposes)
		}
		for i := range c.Runtimes {
			slices.Sort(c.Runtimes[i].DeclaredPurposes)
		}
		return c
	}
	next, previous = normalize(next), normalize(previous)
	for _, n := range next.Agents {
		for _, old := range previous.Agents {
			if n.LocalID == old.LocalID && (n.Revision < old.Revision || n.Revision == old.Revision && !reflect.DeepEqual(n, old)) {
				return invalid("definition_revision_required", "$.agents")
			}
		}
	}
	for _, n := range next.Runtimes {
		for _, old := range previous.Runtimes {
			if n.ID == old.ID && (n.Revision < old.Revision || n.Revision == old.Revision && !reflect.DeepEqual(n, old)) {
				return invalid("binding_revision_required", "$.runtimes")
			}
		}
	}
	return nil
}
