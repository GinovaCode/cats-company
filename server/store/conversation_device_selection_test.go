package store

import (
	"strings"
	"testing"
)

func TestDeviceSelectionScopeValidation(t *testing.T) {
	for _, scope := range []struct {
		actor   int64
		session string
	}{{0, "session"}, {7, ""}, {7, strings.Repeat("x", 769)}, {7, "非规范会话"}} {
		if ValidateDeviceSelectionScope(scope.actor, scope.session) == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	if err := ValidateDeviceSelectionScope(7, "session:v2:catscompany:p2p:p2p_7_42:agent:usr42"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeviceSelectionRecord("", ConversationDeviceSelection{DeviceID: "", Revision: "revision", UpdatedAt: 1}); err != nil {
		t.Fatal("empty version slot should be valid", err)
	}
	if ValidateDeviceSelectionRecord("", ConversationDeviceSelection{DeviceID: "a", UpdatedAt: 1}) == nil {
		t.Fatal("unversioned record accepted")
	}
}
