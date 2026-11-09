package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// k8sPodLogs returns the logs of the workspace pod labelled igloo.dev/workspace-id=<id>,
// read through the Kubernetes API with the server's in-cluster service account.
func k8sPodLogs(ctx context.Context, workspaceID string) (string, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return "", fmt.Errorf("not running in a Kubernetes cluster")
	}
	token, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return "", fmt.Errorf("read service account token: %w", err)
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return "", fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)

	ns := os.Getenv("IGLOO_WORKSPACE_NAMESPACE")
	if ns == "" {
		ns = "workspaces"
	}
	base := "https://" + host + ":" + port
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}

	get := func(path string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		return body, nil
	}

	sel := url.QueryEscape("igloo.dev/workspace-id=" + workspaceID)
	raw, err := get("/api/v1/namespaces/" + ns + "/pods?labelSelector=" + sel)
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &pods); err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pod for workspace")
	}

	out, err := get("/api/v1/namespaces/" + ns + "/pods/" + pods.Items[0].Metadata.Name + "/log?timestamps=true&tailLines=2000")
	if err != nil {
		return "", fmt.Errorf("pod logs: %w", err)
	}
	return string(out), nil
}
