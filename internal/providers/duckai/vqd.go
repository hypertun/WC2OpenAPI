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

	"github.com/dop251/goja"
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

// vqdScriptResult represents the structure returned by the DDG VQD script
type vqdScriptResult struct {
	ServerHashes []string      `json:"server_hashes"`
	ClientHashes []string      `json:"client_hashes"`
	Signals      interface{}   `json:"signals"`
	Meta         vqdMeta       `json:"meta"`
}

type vqdMeta struct {
	V           string `json:"v"`
	ChallengeID string `json:"challenge_id"`
	Timestamp   string `json:"timestamp"`
	Debug       string `json:"debug"`
}

func (c *Client) computeVqdHash(scriptB64 string) (string, error) {
	jsScript, err := base64.StdEncoding.DecodeString(scriptB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode VQD script: %w", err)
	}

	scriptStr := string(jsScript)

	// Create goja VM
	vm := goja.New()
	global := vm.GlobalObject()

	// Set navigator object with UA and webdriver=false
	navigatorObj, _ := vm.RunString(`({
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
		webdriver: false
	})`)
	global.Set("navigator", navigatorObj)

	// Register __makeIframe helper function
	global.Set("__makeIframe", func(call goja.FunctionCall) goja.Value {
		iframe, _ := vm.RunString(`({
			contentDocument: {
				querySelector: function(sel) {
					return {
						getAttribute: function(a) {
							return a === 'content' ? "default-src 'none'; script-src 'unsafe-inline';" : '';
						}
					};
				},
				head: { appendChild: function() {}, removeChild: function() {} },
				body: { appendChild: function() {}, removeChild: function() {} },
				createElement: function(tag) { 
					return { 
						setAttribute: function() {}, 
						appendChild: function() {}, 
						removeChild: function() {},
						getAttribute: function() { return ''; }
					}; 
				}
			},
			contentWindow: { 
				Proxy: function Proxy() {},
				document: null
			},
			getAttribute: function(a) { return a === 'sandbox' ? 'allow-scripts allow-same-origin' : ''; },
			sandbox: { add: function() {} },
			srcdoc: ''
		})`)
		iframeObj := iframe.(*goja.Object)
		contentWindow := iframeObj.Get("contentWindow").(*goja.Object)
		contentDocument := iframeObj.Get("contentDocument")
		contentWindow.Set("document", contentDocument)
		return iframe
	})

	// Define document, window, and browser mocking setup
	setupScript := `
var document = {
	createElement: function(tag) {
		if (tag === 'iframe') return __makeIframe();
		if (tag === 'div') {
			var divObj = {
				_innerHTML: '',
				attributes: {},
				setAttribute: function(k, v) { this.attributes[k] = v; },
				getAttribute: function(k) { return this.attributes[k] || ''; },
				appendChild: function(child) { return child; },
				removeChild: function(child) { return child; },
				querySelectorAll: function(sel) {
					var html = this._innerHTML, count = 0, i = 0;
					if (typeof html === 'string') {
						while(i < html.length-1) {
							if(html[i]==='<' && html[i+1]!=='/') count++;
							i++;
						}
					}
					return { length: count };
				}
			};
			// Use Object.defineProperty for reliable getter/setter
			Object.defineProperty(divObj, 'innerHTML', {
				get: function() { return this._innerHTML; },
				set: function(v) { this._innerHTML = v || ''; },
				enumerable: true,
				configurable: true
			});
			return divObj;
		}
		var genericEl = {
			attributes: {},
			setAttribute: function(k, v) { this.attributes[k] = v; },
			getAttribute: function(k) { return this.attributes[k] || ''; },
			appendChild: function(child) { return child; },
			removeChild: function(child) { return child; }
		};
		return genericEl;
	},
	querySelector: function(sel) { 
		if (sel === '#jsa') return __makeIframe(); 
		return null; 
	},
	querySelectorAll: function(sel) {
		return { length: 0 };
	},
	body: { 
		appendChild: function(){}, 
		removeChild: function(){},
		attributes: {},
		children: [],
		length: 0
	}
};
var window = globalThis;
window.__DDG_BE_VERSION__ = 1;
window.__DDG_FE_CHAT_HASH__ = 1;
window.top = window;
window.self = window;
	window.document = document;
window.top = window;
var __origKeys = Object.keys;
Object.keys = function(obj) {
	if (obj === window) return ['__DDG_BE_VERSION__', '__DDG_FE_CHAT_HASH__'];
	return __origKeys(obj);
};
// Add more complete object references and symbols
Error.symbol = undefined;
Object.freeze = function(obj) { return obj; };
if (!window.Symbol) window.Symbol = {};
window.HTMLDivElement = function() {};
window.HTMLElement = function() {};
window.Element = function() {};
window.customElements = { define: function() {} };
`

	_, err = vm.RunString(setupScript)
	if err != nil {
		return "", fmt.Errorf("failed to setup goja environment: %w", err)
	}

	// Wrap script execution with promise handler
	wrappedScript := fmt.Sprintf(`
var __vqd_result = null;
var __vqd_error = null;
(%s).then(function(r) { __vqd_result = JSON.stringify(r); })
	.catch(function(e) { __vqd_error = e.toString(); });
`, scriptStr)

	_, err = vm.RunString(wrappedScript)
	if err != nil {
		return "", fmt.Errorf("failed to execute VQD script: %w", err)
	}

	// Extract result
	resultVal := vm.Get("__vqd_result")
	if resultVal == nil || goja.IsUndefined(resultVal) || goja.IsNull(resultVal) {
		errVal := vm.Get("__vqd_error")
		if errVal != nil && !goja.IsUndefined(errVal) {
			return "", fmt.Errorf("VQD script execution error: %v", errVal)
		}
		return "", fmt.Errorf("VQD script produced no result")
	}

	resultJSON := resultVal.String()

	// Debug: Log the raw result from the script before processing
	slog.Debug("VQD script raw result",
		"raw_json", resultJSON)

	// Parse result into map to preserve array values
	var rawResult map[string]json.RawMessage
	err = json.Unmarshal([]byte(resultJSON), &rawResult)
	if err != nil {
		return "", fmt.Errorf("failed to parse VQD script result: %w", err)
	}

	// Build proper result structure
	var scriptResult vqdScriptResult
	err = json.Unmarshal([]byte(resultJSON), &scriptResult)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal VQD result: %w", err)
	}

	// SHA256-hash all client_hashes
	hashedHashes := make([]string, len(scriptResult.ClientHashes))
	for i, h := range scriptResult.ClientHashes {
		sum := sha256.Sum256([]byte(h))
		hashedHashes[i] = base64.StdEncoding.EncodeToString(sum[:])
	}
	scriptResult.ClientHashes = hashedHashes

	// Serialize to JSON with ordered fields
	encoded, err := json.Marshal(scriptResult)
	if err != nil {
		return "", fmt.Errorf("failed to marshal VQD result: %w", err)
	}

	resultB64 := base64.StdEncoding.EncodeToString(encoded)

	// Debug: Log the final encoded JSON
	ch0Prefix := "too_short"
	if len(scriptResult.ClientHashes) > 0 && len(scriptResult.ClientHashes[0]) > 20 {
		ch0Prefix = scriptResult.ClientHashes[0][:20]
	}
	slog.Debug("VQD final result before base64",
		"final_json", string(encoded),
		"client_hashes_0_prefix", ch0Prefix)

	// Log details for debugging
	cid := scriptResult.Meta.ChallengeID
	if len(cid) > 30 {
		cid = cid[:30] + "..."
	}
	sh0 := ""
	if len(scriptResult.ServerHashes) > 0 {
		sh0 = scriptResult.ServerHashes[0]
		if len(sh0) > 20 {
			sh0 = sh0[:20] + "..."
		}
	}
	b64pre := resultB64
	if len(b64pre) > 50 {
		b64pre = b64pre[:50]
	}

	slog.Debug("VQD hash computed",
		"encoded_json_len", len(encoded),
		"b64_len", len(resultB64),
		"server_hashes_count", len(scriptResult.ServerHashes),
		"client_hashes_count", len(scriptResult.ClientHashes),
		"server_hashes[0]", sh0,
		"challenge_id", cid,
		"timestamp", scriptResult.Meta.Timestamp,
		"b64_preview", b64pre)

	return resultB64, nil
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
