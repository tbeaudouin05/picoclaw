package school

import (
	"encoding/json"
	"fmt"
	"strings"
)

func ApplyDotPathUpdates(base json.RawMessage, updates map[string]any) (json.RawMessage, []string, error) {
	if !json.Valid(base) {
		return nil, nil, fmt.Errorf("base config is not valid JSON")
	}
	var root map[string]any
	if err := json.Unmarshal(base, &root); err != nil {
		return nil, nil, err
	}
	changed := make([]string, 0, len(updates))
	for path, value := range updates {
		path = strings.TrimSpace(path)
		if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
			return nil, nil, fmt.Errorf("invalid update path %q", path)
		}
		parts := strings.Split(path, ".")
		cur := root
		for _, part := range parts[:len(parts)-1] {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, nil, fmt.Errorf("invalid update path %q", path)
			}
			next, ok := cur[part].(map[string]any)
			if !ok {
				if _, exists := cur[part]; exists {
					return nil, nil, fmt.Errorf("invalid update path %q: %q is not an object", path, part)
				}
				next = map[string]any{}
				cur[part] = next
			}
			cur = next
		}
		leaf := strings.TrimSpace(parts[len(parts)-1])
		if leaf == "" {
			return nil, nil, fmt.Errorf("invalid update path %q", path)
		}
		cur[leaf] = value
		changed = append(changed, path)
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return out, changed, nil
}
