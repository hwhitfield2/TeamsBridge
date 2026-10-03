package connector

import "encoding/json"

// bridgev2 can save metadata outside the client's auth lock. Serialize under
// the same lock used for writes, including clients sharing restored metadata.
func (m *Metadata) MarshalJSON() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type wire Metadata
	return json.Marshal((*wire)(m))
}
