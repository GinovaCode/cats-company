package server

import (
	"os"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

const xiaobaDeviceTargetsCapability = "device-targets.v1"

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

// Gate both metadata issuance and grant consumption by the authenticated Bot
// UID. A scoped rollout must not send v2 metadata to unrelated old consumers.
// Entries are canonical positive decimal UIDs, not names or user-provided IDs.
func xiaobaRuntimeV2EnabledForBot(botUID int64) bool {
	if xiaobaRuntimeV2Enabled() {
		return true
	}
	if botUID <= 0 {
		return false
	}
	want := strconv.FormatInt(botUID, 10)
	for _, entry := range strings.Split(os.Getenv("CATSCO_XIAOBA_RUNTIME_V2_BOT_UIDS"), ",") {
		if strings.TrimSpace(entry) == want {
			return true
		}
	}
	return false
}

// xiaobaRuntimeV2EnabledForClient is the compatibility boundary for the
// additive multi-device protocol. The operator controls the rollout gate, but
// the currently connected, authenticated Bot must opt in before it receives or
// can consume explicit-device capabilities. Older clients omit the handshake
// field and therefore remain on the selected-device v1 path even after a
// global compatible rollout is enabled.
func xiaobaRuntimeV2EnabledForClient(client *Client) bool {
	return client != nil &&
		client.accountType == types.AccountBot &&
		xiaobaRuntimeV2EnabledForBot(client.uid) &&
		client.hasRuntimeCapability(xiaobaDeviceTargetsCapability)
}

func deviceGrantEnforcementRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_DEVICE_GRANT_ENFORCEMENT"))) {
	case "1", "true", "yes", "on", "enabled", "required", "strict":
		return true
	default:
		return false
	}
}
