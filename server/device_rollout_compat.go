package server

import (
	"os"
	"strings"
)

// Multi-device metadata and grant enforcement are rolled out independently.
// The defaults keep older cloud Bots and XiaoBa clients functional while the
// paired clients are upgraded. Operators can enable each strict protocol
// explicitly after the matching clients are deployed. V2 also opts into the
// separate explicit-device capability envelope; the legacy device_grants list
// remains selected-only in both states.
func xiaobaRuntimeV2Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED"))) {
	case "1", "true", "yes", "on", "enabled", "required":
		return true
	default:
		return false
	}
}

func deviceGrantEnforcementRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_DEVICE_GRANT_ENFORCEMENT"))) {
	case "1", "true", "yes", "on", "enabled", "required", "strict":
		return true
	default:
		return false
	}
}
