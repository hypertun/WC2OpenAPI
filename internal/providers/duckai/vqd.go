package duckai

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// findChromiumBrowser searches for any available Chromium-based browser
func findChromiumBrowser() string {
	var candidates []string

	switch runtime.GOOS {
	case "darwin": // macOS
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Opera.app/Contents/MacOS/Opera",
		}
	case "windows":
		candidates = []string{
			"C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
			"C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
			"C:\\Program Files\\Chromium\\Application\\chrome.exe",
			"C:\\Program Files (x86)\\Chromium\\Application\\chrome.exe",
			"C:\\Program Files\\BraveSoftware\\Brave-Browser\\Application\\brave.exe",
			"C:\\Program Files (x86)\\BraveSoftware\\Brave-Browser\\Application\\brave.exe",
			"C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe",
			"C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe",
			"C:\\Program Files\\Opera\\opera.exe",
		}
	case "linux":
		candidates = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/chromium-browser",
			"/usr/bin/chromium",
			"/snap/bin/chromium",
			"/usr/bin/brave-browser",
			"/usr/bin/microsoft-edge",
			"/usr/bin/opera",
		}
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// getKeys returns the keys from a map as a slice of strings (for debug logging)
func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// jsonStringify converts a string to a JSON-quoted string for safe JavaScript embedding
func jsonStringify(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (c *Client) getVQD(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL.String()+statusURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create VQD request: %w", err)
	}

	setCommonHeaders(req)
	req.Header.Set("x-vqd-accept", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("VQD request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("VQD request returned %d: %s", resp.StatusCode, string(body))
	}

	hashHeader := resp.Header.Get("x-Vqd-hash-1")
	if hashHeader == "" {
		return "", fmt.Errorf("missing x-Vqd-hash-1 header in VQD response")
	}

	return c.computeVqdHash(hashHeader)
}

func (c *Client) computeVqdHash(scriptB64 string) (string, error) {
	jsScript, err := base64.StdEncoding.DecodeString(scriptB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode VQD script: %w", err)
	}

	scriptStr := string(jsScript)

	// Use chromedp to execute the script in a real browser environment
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find an available Chromium-based browser
	browserPath := findChromiumBrowser()
	if browserPath == "" {
		return "", fmt.Errorf("no Chromium-based browser found (Chrome, Edge, Brave, Opera, Chromium). Please install one to enable VQD hash computation")
	}

	// Create chromedp allocator with the found browser
	allocatorCtx, cancel := chromedp.NewExecAllocator(ctx,
		chromedp.ExecPath(browserPath),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,
		chromedp.Flag("headless", "new"),
	)
	defer cancel()

	allocCtx, cancel := chromedp.NewContext(allocatorCtx)
	defer cancel()

	// Navigate to about:blank first
	err = chromedp.Run(allocCtx,
		chromedp.Navigate("about:blank"),
	)
	if err != nil {
		slog.Warn("VQD script navigation failed via chromedp",
			"error", err.Error(),
			"browser_path", browserPath)
		return "", fmt.Errorf("VQD script navigation failed: %w", err)
	}

	// Inject the HTML with the iframe and set up globals and result containers
	// We create the iframe with sandbox attribute and srcdoc containing minimal HTML
	iframeHTML := `<!DOCTYPE html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'"></head><body></body></html>`
	
	injectHTMLScript := fmt.Sprintf(`
		// Create iframe element
		const iframe = document.createElement('iframe');
		iframe.id = 'jsa';
		iframe.sandbox.add('allow-scripts');
		iframe.sandbox.add('allow-same-origin');
		iframe.style.position = 'absolute';
		iframe.style.left = '-9999px';
		iframe.style.top = '-9999px';
		// Set srcdoc to minimal HTML with CSP meta tag
		iframe.srcdoc = %s;
		document.body.appendChild(iframe);
		
		// Set up window globals
		window.__DDG_BE_VERSION__ = 1;
		window.__DDG_FE_CHAT_HASH__ = 1;
		window.__vqdResult = null;
		window.__vqdError = null;
	`, jsonStringify(iframeHTML))

	err = chromedp.Run(allocCtx,
		chromedp.Evaluate(injectHTMLScript, nil),
	)
	if err != nil {
		slog.Warn("VQD HTML injection failed via chromedp",
			"error", err.Error(),
			"browser_path", browserPath)
		return "", fmt.Errorf("VQD HTML injection failed: %w", err)
	}

	// Give the DOM a moment to settle for iframe srcdoc to be processed
	time.Sleep(200 * time.Millisecond)

	// Now inject and run the DDG script
	runVQDScript := fmt.Sprintf(`
		(async function() {
			try {
				window.__vqdResult = await (%s);
			} catch(e) {
				window.__vqdError = e.toString();
			}
		})();
	`, scriptStr)

	err = chromedp.Run(allocCtx,
		chromedp.Evaluate(runVQDScript, nil),
	)
	if err != nil {
		slog.Warn("VQD script execution failed via chromedp",
			"error", err.Error(),
			"browser_path", browserPath)
		return "", fmt.Errorf("VQD script execution failed: %w", err)
	}

	// Poll for the async script result (up to 5 seconds)
	maxWaitTime := 5 * time.Second
	pollInterval := 100 * time.Millisecond
	startTime := time.Now()

	for time.Since(startTime) < maxWaitTime {
		var result interface{}
		var errMsg string

		// Check for error first
		errErr := chromedp.Run(allocCtx,
			chromedp.Evaluate("window.__vqdError", &errMsg),
		)
		if errErr != nil {
			slog.Debug("Error checking window.__vqdError",
				"error", errErr.Error())
		} else if errMsg != "" {
			slog.Warn("VQD script execution error",
				"error", errMsg,
				"browser_path", browserPath)
			return "", fmt.Errorf("VQD script execution error: %s", errMsg)
		}

		// Check for result
		resErr := chromedp.Run(allocCtx,
			chromedp.Evaluate("window.__vqdResult", &result),
		)
		if resErr == nil && result != nil {
			// Result is ready, parse it
			slog.Debug("VQD script result retrieved",
				"result_type", fmt.Sprintf("%T", result),
				"result_value", fmt.Sprintf("%v", result))

			resultMap, ok := result.(map[string]interface{})
			if !ok {
				slog.Debug("VQD result is not a map", "type", fmt.Sprintf("%T", result), "value", result)
				return "", fmt.Errorf("VQD script returned unexpected type: %T", result)
			}

			clientHashesRaw, ok := resultMap["client_hashes"]
			if !ok {
				slog.Debug("VQD result keys", "keys", getKeys(resultMap))
				return "", fmt.Errorf("VQD result missing client_hashes")
			}

			// Handle both []interface{} and []string
			var clientHashes []string
			switch v := clientHashesRaw.(type) {
			case []interface{}:
				clientHashes = make([]string, len(v))
				for i, item := range v {
					clientHashes[i] = fmt.Sprintf("%v", item)
				}
			case []string:
				clientHashes = v
			default:
				slog.Debug("VQD client_hashes type", "type", fmt.Sprintf("%T", clientHashesRaw))
				return "", fmt.Errorf("VQD client_hashes is unexpected type: %T", clientHashesRaw)
			}

			if len(clientHashes) == 0 {
				return "", fmt.Errorf("VQD returned empty client_hashes")
			}

			// Update first hash to Chrome user agent
			chromeUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
			clientHashes[0] = chromeUA

			// SHA256 hash each client hash
			hashed := make([]string, len(clientHashes))
			for i, h := range clientHashes {
				sum := sha256.Sum256([]byte(h))
				hashed[i] = base64.StdEncoding.EncodeToString(sum[:])
			}

			out := map[string]interface{}{
				"client_hashes": hashed,
			}

			// Copy other fields from result
			for _, k := range []string{"vqd", "dict", "server_hashes", "signals", "meta"} {
				if v, ok := resultMap[k]; ok {
					out[k] = v
				}
			}

			encoded, err := json.Marshal(out)
			if err != nil {
				return "", fmt.Errorf("failed to marshal VQD result: %w", err)
			}

			return base64.StdEncoding.EncodeToString(encoded), nil
		}

		// Not ready yet, wait and retry
		time.Sleep(pollInterval)
	}

	// Timeout waiting for result
	slog.Warn("VQD script result retrieval timed out via chromedp",
		"browser_path", browserPath)
	return "", fmt.Errorf("VQD script result retrieval timed out after %v", maxWaitTime)
}


func setCommonHeaders(req *http.Request) {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
	req.Header.Set("User-Agent", ua)
	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("cache-control", "no-store")
	req.Header.Set("pragma", "no-cache")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("referer", "https://duckduckgo.com/")

	isChat := strings.Contains(req.URL.Path, "/duckchat/v1/chat")
	if isChat {
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-fe-version", "serp_20250401_100419_ET-19d438eb199b2bf7c300")
	}
}
