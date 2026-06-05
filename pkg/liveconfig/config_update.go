package liveconfig

import (
	"encoding/json"
	"fmt"
	"sort"
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
	paths := make([]string, 0, len(updates))
	valuesByPath := make(map[string]any, len(updates))
	for rawPath, value := range updates {
		path := strings.TrimSpace(rawPath)
		if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
			return nil, nil, fmt.Errorf("invalid update path %q", rawPath)
		}
		if _, exists := valuesByPath[path]; exists {
			return nil, nil, fmt.Errorf("duplicate update path %q", path)
		}
		paths = append(paths, path)
		valuesByPath[path] = value
	}
	sort.Strings(paths)
	for i, path := range paths {
		for _, other := range paths[i+1:] {
			if strings.HasPrefix(other, path+".") {
				return nil, nil, fmt.Errorf("overlapping update paths %q and %q are not allowed", path, other)
			}
		}
	}
	for _, path := range paths {
		value := valuesByPath[path]
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
