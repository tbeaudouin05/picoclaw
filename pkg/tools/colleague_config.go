package tools

import "sync"

// colleagueConfigMu serializes config mutations performed by colleague-management
// tools so concurrent invocations cannot race on cfg.Agents.List or on the
// SaveConfig write. The mutex is package-level because cfg is shared across all
// tool instances registered with the AgentLoop.
var colleagueConfigMu sync.Mutex
