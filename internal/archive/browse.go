package archive

import "slices"

// SupportedKinds describes this historical adapter without exposing table names.
func (r *Reader) SupportedKinds() []string {
	kinds := []string{}
	for kind, table := range objectTables {
		if table.minimum <= r.manifest.Schema {
			kinds = append(kinds, kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}

// RecordID returns this adapter's stable cursor identity, without a second table map.
func (r *Reader) RecordID(kind string, record map[string]any) (string, bool) {
	table, ok := objectTables[kind]
	if !ok || table.minimum > r.manifest.Schema {
		return "", false
	}
	id, ok := record[table.id].(string)
	return id, ok
}
