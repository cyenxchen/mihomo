package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/metacubex/mihomo/constant/features"
	"github.com/metacubex/mihomo/log"

	"go.yaml.in/yaml/v3"
)

func forkDeviceName() string {
	if name, ok := os.LookupEnv("MIHOMO_DEVICE_NAME"); ok {
		return strings.TrimSpace(name)
	}
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		name, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "ComputerName").Output()
		if err == nil {
			return strings.TrimSpace(string(name))
		}
	case "android":
		// Android's hostname is not its user-assigned device name.
	default:
		name, _ := os.Hostname()
		return strings.TrimSpace(name)
	}
	return ""
}

func forkDeviceDocument(buf []byte) (*yaml.Node, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(buf, &doc); err != nil || len(doc.Content) == 0 {
		return nil, false, err
	}
	root := doc.Content[0]
	var header struct {
		Overrides yaml.Node `yaml:"device-overrides"`
	}
	if err := root.Decode(&header); err != nil {
		return nil, false, err
	}
	overrides := &header.Overrides
	if overrides.Kind == yaml.AliasNode && overrides.Alias != nil {
		overrides = overrides.Alias
	}
	switch overrides.Kind {
	case 0:
		return root, false, nil
	case yaml.ScalarNode:
		var value any
		if overrides.ShortTag() == "!!null" && overrides.Decode(&value) == nil {
			return root, false, nil
		}
		return root, true, nil
	case yaml.SequenceNode:
		return root, len(overrides.Content) > 0, nil
	default:
		return root, true, nil
	}
}

func forkLoadDeviceConfig(buf []byte, cfg *RawConfig) error {
	return forkUnmarshalDeviceConfig(buf, cfg, runtime.GOOS == "android" && features.CMFA)
}

func forkUnmarshalDeviceConfig(buf []byte, cfg *RawConfig, legacyAndroid bool) error {
	root, hasOverrides, err := forkDeviceDocument(buf)
	if err != nil || root == nil {
		return err
	}
	if hasOverrides {
		if legacyAndroid {
			log.Warnln("[DeviceOverrides] using base configuration: Android client must expand device-overrides before preparing the VPN")
		} else {
			cfg.hasDeviceOverrides = true
			root, err = forkApplyDeviceOverrides(root, runtime.GOOS, forkDeviceName())
			if err != nil {
				return err
			}
		}
	}
	return root.Decode(cfg)
}

// ExpandDeviceOverrides resolves a plaintext profile for an embedding client before
// it derives OS settings or applies its own patches. An empty name matches no device name.
func ExpandDeviceOverrides(buf []byte, goos, deviceName string) ([]byte, error) {
	root, hasOverrides, err := forkDeviceDocument(buf)
	if err != nil {
		return nil, err
	}
	if !hasOverrides {
		return buf, nil
	}
	root, err = forkApplyDeviceOverrides(root, goos, strings.TrimSpace(deviceName))
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(root)
}

type forkDeviceOverride struct {
	When struct {
		OS   yaml.Node `yaml:"os"`
		Name yaml.Node `yaml:"device-name"`
	} `yaml:"when"`
	Set     yaml.Node `yaml:"set"`
	Prepend struct {
		Rules yaml.Node `yaml:"rules"`
	} `yaml:"prepend"`
}

func forkStrictDeviceDecode(n *yaml.Node, value any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	return d.Decode(value)
}

func forkApplyDeviceOverrides(root *yaml.Node, goos, name string) (*yaml.Node, error) {
	root, err := forkDeviceMapping(root)
	if err != nil {
		return nil, fmt.Errorf("prepare YAML for device-overrides: %w", err)
	}
	idx := forkDeviceKey(root, "device-overrides")
	if idx < 0 {
		return nil, fmt.Errorf("device-overrides missing after YAML merge")
	}
	blocks := root.Content[idx+1]
	root.Content = slices.Delete(root.Content, idx, idx+2)
	if blocks.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("device-overrides must be a sequence")
	}
	var matched []int
	var rules []*yaml.Node
	for i, block := range blocks.Content {
		ok, prepend, err := forkApplyDeviceBlock(root, block, goos, name)
		if err != nil {
			return nil, fmt.Errorf("device-overrides[%d] (source line %d): %w", i, block.Line, err)
		}
		if ok {
			matched = append(matched, i)
			rules = append(rules, prepend...)
		}
	}
	if len(rules) > 0 {
		idx := forkDeviceKey(root, "rules")
		if idx < 0 {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "rules"}, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
			idx = len(root.Content) - 2
		}
		base := root.Content[idx+1]
		if base.ShortTag() == "!!null" {
			base = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			root.Content[idx+1] = base
		}
		if base.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("device-overrides: cannot prepend to non-sequence rules")
		}
		base.Content = append(rules, base.Content...)
	}
	log.Infoln("[DeviceOverrides] os=%s device-name=%q matched=%v", goos, name, matched)
	return root, nil
}

func forkApplyDeviceBlock(root, block *yaml.Node, goos, name string) (bool, []*yaml.Node, error) {
	var override forkDeviceOverride
	if err := forkStrictDeviceDecode(block, &override); err != nil {
		return false, nil, err
	}
	when := &override.When
	if when.OS.Kind == 0 && when.Name.Kind == 0 {
		return false, nil, fmt.Errorf("when requires os or device-name")
	}
	osMatch, err := forkDeviceMatches(&when.OS, goos)
	if err != nil {
		return false, nil, fmt.Errorf("when.os: %w", err)
	}
	nameMatch, err := forkDeviceMatches(&when.Name, name)
	if err != nil {
		return false, nil, fmt.Errorf("when.device-name: %w", err)
	}
	rules := &override.Prepend.Rules
	if override.Set.Kind == 0 && rules.Kind == 0 {
		return false, nil, fmt.Errorf("expected set or prepend.rules")
	}
	if rules.Kind != 0 {
		if rules.Kind != yaml.SequenceNode {
			return false, nil, fmt.Errorf("prepend.rules must be a sequence")
		}
		for _, rule := range rules.Content {
			if rule.ShortTag() != "!!str" || strings.TrimSpace(rule.Value) == "" {
				return false, nil, fmt.Errorf("prepend.rules requires non-empty strings")
			}
		}
	}
	match := osMatch && nameMatch
	if override.Set.Kind != 0 {
		if override.Set.Kind != yaml.MappingNode {
			return false, nil, fmt.Errorf("set must be a mapping")
		}
		if err := forkStrictDeviceDecode(&override.Set, &RawConfig{}); err != nil {
			return false, nil, fmt.Errorf("set: %w", err)
		}
		if match {
			forkMergeDeviceMapping(root, &override.Set)
		}
	}
	return match, rules.Content, nil
}

func forkDeviceMatches(condition *yaml.Node, actual string) (bool, error) {
	if condition.Kind == 0 {
		return true, nil
	}
	values := condition.Content
	switch condition.Kind {
	case yaml.ScalarNode:
		values = []*yaml.Node{condition}
	case yaml.SequenceNode:
		if len(values) == 0 {
			return false, fmt.Errorf("empty list")
		}
	default:
		return false, fmt.Errorf("expected a string or a list of strings")
	}
	found := false
	for _, value := range values {
		s := strings.TrimSpace(value.Value)
		if value.ShortTag() != "!!str" || s == "" {
			return false, fmt.Errorf("expected non-empty strings")
		}
		if s == actual {
			found = true
		}
	}
	return found, nil
}
