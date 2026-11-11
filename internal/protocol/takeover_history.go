package protocol

// TakeoverObservationFromHistoricalEvent decodes one persisted takeover's
// original provenance. This is a dedicated history reader, independent of the
// retiring wire adapter. It does not preserve RPC state or a request map.
func TakeoverObservationFromHistoricalEvent(run, request string) string {
	if !ValidID(PrefixRun, run) || !ValidID(PrefixRequest, request) {
		return ""
	}
	return NativeObjectID(PrefixRequest, "legacy-takeover:"+run, request)
}
