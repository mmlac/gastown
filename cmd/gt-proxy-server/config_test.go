package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	t.Run("parses all fields correctly", func(t *testing.T) {
		cfg := ProxyConfig{
			ListenAddr:      "0.0.0.0:9876",
			CADir:           "/tmp/ca",
			TownRoot:        "/tmp/gt",
			AllowedCommands: []string{"gt", "bd"},
			AllowedSubcommands: map[string][]string{
				"gt": {"prime", "hook"},
				"bd": {"create", "show"},
			},
			ExtraSANIPs:   []string{"170.170.170.170", "10.8.0.1"},
			ExtraSANHosts: []string{"proxy.mycompany.com", "gt-proxy.local"},
		}
		data, err := json.Marshal(cfg)
		require.NoError(t, err)

		path := filepath.Join(t.TempDir(), "config.json")
		require.NoError(t, os.WriteFile(path, data, 0644))

		got, err := loadConfig(path)
		require.NoError(t, err)

		assert.Equal(t, "0.0.0.0:9876", got.ListenAddr)
		assert.Equal(t, "/tmp/ca", got.CADir)
		assert.Equal(t, "/tmp/gt", got.TownRoot)
		assert.Equal(t, []string{"gt", "bd"}, got.AllowedCommands)
		assert.Equal(t, []string{"prime", "hook"}, got.AllowedSubcommands["gt"])
		assert.Equal(t, []string{"create", "show"}, got.AllowedSubcommands["bd"])
		assert.Equal(t, []string{"170.170.170.170", "10.8.0.1"}, got.ExtraSANIPs)
		assert.Equal(t, []string{"proxy.mycompany.com", "gt-proxy.local"}, got.ExtraSANHosts)
	})
}

func TestLoadConfigMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	cfg, err := loadConfig(path)
	require.NoError(t, err, "missing config file should not return error")
	assert.Equal(t, ProxyConfig{}, cfg, "missing config file should return empty ProxyConfig")
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0644))

	_, err := loadConfig(path)
	assert.Error(t, err, "malformed JSON should return error")
}

func TestLoadConfigInvalidIP(t *testing.T) {
	// Invalid IPs in extra_san_ips are validated in main(), not loadConfig().
	// loadConfig only deserialises the JSON — the IP strings are returned as-is.
	// This test verifies that loadConfig itself does not error on invalid IP strings.
	data := []byte(`{"extra_san_ips": ["not-an-ip", "256.256.256.256", "192.0.2.1"]}`)
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, data, 0644))

	cfg, err := loadConfig(path)
	require.NoError(t, err, "loadConfig should not error on invalid IP strings")
	assert.Equal(t, []string{"not-an-ip", "256.256.256.256", "192.0.2.1"}, cfg.ExtraSANIPs)
}
