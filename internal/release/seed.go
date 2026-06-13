package release

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed agent-seed.json
var agentSeedJSON []byte

// agentSeedData is the raw shape of agent-seed.json.
type agentSeedData struct {
	Releases map[string]AgentRelease `json:"releases"`
}

// AgentSeed returns the checked-in history of backend releases used by the
// release workflow to build the agent section of versions.json.
//
// It panics on bad embed data (should never happen in a correctly built binary).
func AgentSeed() map[string]AgentRelease {
	var s agentSeedData
	if err := json.Unmarshal(agentSeedJSON, &s); err != nil {
		panic(fmt.Sprintf("release: malformed agent-seed.json embed: %v", err))
	}
	return s.Releases
}
