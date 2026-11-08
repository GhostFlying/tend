package traex

import "testing"

func TestNativeMCPDiagnosticsDoNotInferConnectionFromCachedTools(t *testing.T) {
	for _, test := range []struct {
		runtime    string
		auth       string
		toolsError bool
		tools      int
		want       string
	}{
		{"", "unknown", false, 3, "catalog_loaded_connection_unverified"},
		{"failed", "bearerToken", false, 3, "connection_failed"},
		{"connected", "notLoggedIn", false, 3, "authentication_missing"},
		{"connected", "bearerToken", true, 0, "tool_loading_failed"},
		{"connected", "bearerToken", false, 0, "connected_no_tools"},
		{"connected", "bearerToken", false, 3, "connected_tools_loaded"},
		{"disabled", "unknown", false, 3, "disabled_by_policy"},
	} {
		if got := nativeMCPState(test.runtime, test.auth, test.toolsError, test.tools); got != test.want {
			t.Fatalf("runtime=%s auth=%s: got %s want %s", test.runtime, test.auth, got, test.want)
		}
	}
}
