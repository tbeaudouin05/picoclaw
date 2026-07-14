package liveconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func ApplyDotPathUpdates(base json.RawMessage, updates map[string]any) (json.RawMessage, []string, error) {
	return ApplyDotPathChanges(base, updates, nil)
}

func ApplyDotPathChanges(
	base json.RawMessage,
	updates map[string]any,
	deletes []string,
) (json.RawMessage, []string, error) {
	if !json.Valid(base) {
		return nil, nil, fmt.Errorf("base config is not valid JSON")
	}
	var root map[string]any
	if err := json.Unmarshal(base, &root); err != nil {
		return nil, nil, err
	}
	paths := make([]string, 0, len(updates)+len(deletes))
	valuesByPath := make(map[string]any, len(updates))
	deleteByPath := make(map[string]struct{}, len(deletes))
	for rawPath, value := range updates {
		path, err := validateDotPath(rawPath)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := valuesByPath[path]; exists {
			return nil, nil, fmt.Errorf("duplicate update path %q", path)
		}
		valuesByPath[path] = value
		paths = append(paths, path)
	}
	for _, rawPath := range deletes {
		path, err := validateDotPath(rawPath)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := deleteByPath[path]; exists {
			return nil, nil, fmt.Errorf("duplicate delete path %q", path)
		}
		deleteByPath[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for i, path := range paths {
		for _, other := range paths[i+1:] {
			if strings.HasPrefix(other, path+".") || other == path {
				return nil, nil, fmt.Errorf("overlapping update paths %q and %q are not allowed", path, other)
			}
		}
	}
	changed := make([]string, 0, len(paths))
	for _, path := range paths {
		parts := strings.Split(path, ".")
		cur := root
		valid := true
		for _, part := range parts[:len(parts)-1] {
			next, ok := cur[part].(map[string]any)
			if !ok {
				if _, deleting := deleteByPath[path]; deleting {
					valid = false
					break
				}
				if _, exists := cur[part]; exists {
					return nil, nil, fmt.Errorf("invalid update path %q: %q is not an object", path, part)
				}
				next = map[string]any{}
				cur[part] = next
			}
			cur = next
		}
		if !valid {
			continue
		}
		leaf := parts[len(parts)-1]
		if _, deleting := deleteByPath[path]; deleting {
			if _, ok := cur[leaf]; ok {
				delete(cur, leaf)
				changed = append(changed, path)
			}
			continue
		}
		cur[leaf] = valuesByPath[path]
		changed = append(changed, path)
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return out, changed, nil
}

func validateDotPath(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
		return "", fmt.Errorf("invalid update path %q", rawPath)
	}
	for _, part := range strings.Split(path, ".") {
		if strings.TrimSpace(part) == "" {
			return "", fmt.Errorf("invalid update path %q", rawPath)
		}
	}
	return path, nil
}
