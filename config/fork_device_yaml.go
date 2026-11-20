package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

func forkDeviceKey(mapping *yaml.Node, key string) int {
	want := forkDeviceKeyID{kind: yaml.ScalarNode, value: key}
	for i := 0; i < len(mapping.Content); i += 2 {
		node := mapping.Content[i]
		if node.Kind != yaml.ScalarNode {
			continue
		}
		if id, err := forkDeviceKeyIdentity(node); err == nil && id == want {
			return i
		}
	}
	return -1
}

func forkMergeDeviceMapping(dst, patch *yaml.Node) {
	positions := make(map[forkDeviceKeyID]int, len(dst.Content)/2)
	for i := 0; i < len(dst.Content); i += 2 {
		id, _ := forkDeviceKeyIdentity(dst.Content[i])
		positions[id] = i
	}
	for i := 0; i < len(patch.Content); i += 2 {
		key, value := patch.Content[i], patch.Content[i+1]
		id, _ := forkDeviceKeyIdentity(key)
		pos, ok := positions[id]
		if !ok {
			dst.Content = append(dst.Content, key, value)
			continue
		}
		if dst.Content[pos+1].Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			forkMergeDeviceMapping(dst.Content[pos+1], value)
		} else {
			dst.Content[pos+1] = value
		}
	}
}

type forkDeviceKeyID struct {
	kind  yaml.Kind
	value string
}

func forkDeviceKeyIdentity(key *yaml.Node) (forkDeviceKeyID, error) {
	id := forkDeviceKeyID{kind: key.Kind, value: key.Value}
	if key.Kind == yaml.ScalarNode {
		if key.ShortTag() == "!!binary" {
			if err := key.Decode(&id.value); err != nil {
				return id, err
			}
		}
	} else {
		var structure strings.Builder
		forkWriteDeviceKey(&structure, key)
		id.value = structure.String()
	}
	return id, nil
}

func forkWriteDeviceKey(structure *strings.Builder, node *yaml.Node) {
	structure.WriteByte(byte(node.Kind))
	structure.WriteString(strconv.Quote(node.ShortTag()))
	structure.WriteString(strconv.Quote(node.Value))
	structure.WriteByte('[')
	for _, child := range node.Content {
		forkWriteDeviceKey(structure, child)
	}
	structure.WriteByte(']')
}

func forkDeviceMapping(root *yaml.Node) (*yaml.Node, error) {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected YAML mapping")
	}
	expansion := forkDeviceYAML{aliases: make(map[*yaml.Node]bool)}
	return expansion.copy(root, false)
}

type forkDeviceYAML struct {
	aliases    map[*yaml.Node]bool
	aliasNodes int
	aliasBytes int64
}

func (e *forkDeviceYAML) copy(src *yaml.Node, inAlias bool) (*yaml.Node, error) {
	if src.Kind == yaml.AliasNode {
		if src.Alias == nil || e.aliases[src] {
			return nil, fmt.Errorf("line %d: invalid or circular YAML alias %q", src.Line, src.Value)
		}
		e.aliases[src] = true
		resolved, err := e.copy(src.Alias, true)
		delete(e.aliases, src)
		return resolved, err
	}
	clone := *src
	clone.Anchor = ""
	clone.Content = nil
	if inAlias {
		// Marshaling repeats values and tags even if the node copies share strings.
		e.aliasNodes++
		e.aliasBytes += int64(len(src.Value)) + int64(len(src.Tag))
		if e.aliasNodes > 400_000 || e.aliasBytes > 64<<20 {
			return nil, fmt.Errorf("line %d: YAML alias expansion is too large", src.Line)
		}
		clone.HeadComment = ""
		clone.LineComment = ""
		clone.FootComment = ""
	}
	if src.Kind == yaml.MappingNode {
		if err := e.copyMapping(&clone, src, inAlias); err != nil {
			return nil, err
		}
	} else if len(src.Content) > 0 {
		clone.Content = make([]*yaml.Node, 0, len(src.Content))
		for _, child := range src.Content {
			value, err := e.copy(child, inAlias)
			if err != nil {
				return nil, err
			}
			clone.Content = append(clone.Content, value)
		}
	}
	return &clone, nil
}

func (e *forkDeviceYAML) copyMapping(dst, src *yaml.Node, inAlias bool) error {
	if len(src.Content) == 0 {
		return nil
	}
	seen := make(map[forkDeviceKeyID]*yaml.Node, len(src.Content)/2)
	dst.Content = make([]*yaml.Node, 0, len(src.Content))
	var mergeAt int
	var merge *yaml.Node
	for i := 0; i < len(src.Content); i += 2 {
		sourceKey, sourceValue := src.Content[i], src.Content[i+1]
		key, err := e.copy(sourceKey, inAlias)
		if err != nil {
			return err
		}
		id, err := forkDeviceKeyIdentity(key)
		if err != nil {
			return fmt.Errorf("line %d: invalid YAML mapping key: %w", sourceKey.Line, err)
		}
		if first, exists := seen[id]; exists {
			if key.Kind == yaml.ScalarNode {
				return fmt.Errorf("line %d: YAML mapping key %q already defined at line %d", sourceKey.Line, id.value, first.Line)
			}
			return fmt.Errorf("line %d: YAML mapping key already defined at line %d", sourceKey.Line, first.Line)
		}
		seen[id] = sourceKey
		value, err := e.copy(sourceValue, inAlias)
		if err != nil {
			return err
		}
		if sourceKey.Kind == yaml.ScalarNode && key.Value == "<<" && key.ShortTag() == "!!merge" {
			if sourceValue.Kind == yaml.AliasNode && value.Kind != yaml.MappingNode {
				return fmt.Errorf("line %d: YAML merge alias must refer to a mapping", sourceValue.Line)
			}
			merge, mergeAt = value, len(dst.Content)
			continue
		}
		if key.Value == "<<" && key.ShortTag() == "!!merge" {
			key.Tag = "!!str"
		}
		dst.Content = append(dst.Content, key, value)
	}
	if merge == nil {
		return nil
	}
	mappings := merge.Content
	if merge.Kind != yaml.SequenceNode {
		mappings = []*yaml.Node{merge}
	}
	var inherited []*yaml.Node
	for _, mapping := range mappings {
		if mapping.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: YAML merge requires a mapping or a sequence of mappings", mapping.Line)
		}
		for i := 0; i < len(mapping.Content); i += 2 {
			key := mapping.Content[i]
			id, _ := forkDeviceKeyIdentity(key)
			if _, exists := seen[id]; !exists {
				seen[id] = key
				inherited = append(inherited, key, mapping.Content[i+1])
			}
		}
	}
	dst.Content = slices.Insert(dst.Content, mergeAt, inherited...)
	return nil
}
