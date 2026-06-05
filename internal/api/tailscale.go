package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// IssueTailscaleKey calls the Tailscale API to create a one-time ephemeral auth key
// for a workspace. apiKey and tailnet come from the platform config (or env var fallback).
// Returns ("", "", nil) when no API key is available.
func IssueTailscaleKey(apiKey, tailnet, workspaceID, workspaceName string) (key, hostname string, err error) {
	if apiKey == "" {
		apiKey = os.Getenv("TAILSCALE_API_KEY")
	}
	if apiKey == "" {
		return "", "", nil
	}
	if tailnet == "" {
		tailnet = os.Getenv("TAILSCALE_TAILNET")
	}
	if tailnet == "" {
		tailnet = "-"
	}

	hostname = tailscaleHostname(workspaceName, workspaceID)

	body, _ := json.Marshal(map[string]any{
		"capabilities": map[string]any{
			"devices": map[string]any{
				"create": map[string]any{
					"reusable":      false,
					"ephemeral":     true,
					"preauthorized": true,
					"tags":          []string{"tag:igloo-workspace"},
				},
			},
		},
		"expirySeconds": 600,
		"description":   hostname,
	})

	req, _ := http.NewRequest("POST",
		fmt.Sprintf("https://api.tailscale.com/api/v2/tailnet/%s/keys", tailnet),
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("tailscale API: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Key == "" {
		return "", "", fmt.Errorf("tailscale API: no key in response (HTTP %d)", resp.StatusCode)
	}
	return result.Key, hostname, nil
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9-]+`)

func tailscaleHostname(name, id string) string {
	h := strings.ToLower(name)
	h = strings.ReplaceAll(h, " ", "-")
	h = strings.ReplaceAll(h, "_", "-")
	h = nonAlnum.ReplaceAllString(h, "")
	h = strings.Trim(h, "-")
	if h == "" {
		h = id[:8]
	}
	return "igloo-" + h
}
