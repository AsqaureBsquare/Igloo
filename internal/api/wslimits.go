package api

// Largest message each kind of WebSocket peer may send. gorilla/websocket reads a
// whole message into memory, and an unlimited peer can make the server allocate
// without bound. Any code running in a workspace (including an AI agent) holds its
// own agent token and can dial the server, so these limits are what keep one
// workspace from taking the server down. A message over the limit closes the
// connection with status 1009.
const (
	// maxAgentFrame: the workspace agent returns each proxied HTTP response (VS Code
	// assets, downloads) as one message, base64 encoded. The agent caps those bodies
	// at 32 MiB (cmd/agent), which is about 43 MiB encoded.
	maxAgentFrame = 64 << 20

	// maxBrowserFrame: terminal input, resize and similar messages. A large paste is
	// the biggest realistic case.
	maxBrowserFrame = 4 << 20

	// maxProxiedWSFrame: VS Code's own WebSocket traffic relayed through the proxy
	// (file contents opened in the editor).
	maxProxiedWSFrame = 32 << 20

	// maxClientFrame: CLI port-forward data (32 KiB reads) and WireGuard packets
	// (about 1.4 KiB) plus small control messages.
	maxClientFrame = 1 << 20

	// maxDaemonFrame: provisioner daemon log lines, job results and Terraform outputs.
	maxDaemonFrame = 8 << 20
)
