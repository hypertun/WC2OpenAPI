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
	"strings"

	"github.com/dop251/goja"
)

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
				head: { appendChild: function() {} },
				createElement: function() { return { setAttribute: function() {} }; }
			},
			contentWindow: { Proxy: function Proxy() {}, get: function() {} },
			getAttribute: function(a) { return a === 'sandbox' ? 'allow-scripts allow-same-origin' : ''; },
			sandbox: { add: function() {} },
			srcdoc: ''
		})`)
		iframeObj := iframe.(*goja.Object)
		contentWindow := iframeObj.Get("contentWindow").(*goja.Object)
		contentWindow.Set("document", iframeObj.Get("contentDocument"))
		return iframe
	})

	// Define document, window, and browser mocking setup
	setupScript := `
var document = {
	createElement: function(tag) {
		if (tag === 'iframe') return __makeIframe();
		if (tag === 'div') return {
			__innerHTML: '',
			get innerHTML() { return this.__innerHTML; },
			set innerHTML(v) { this.__innerHTML = v; },
			querySelectorAll: function(sel) {
				var html = this.__innerHTML, count = 0, i = 0;
				while(i < html.length-1) {
					if(html[i]==='<' && html[i+1]!=='/') count++;
					i++;
				}
				return { length: count };
			}
		};
		return { setAttribute: function(){}, appendChild: function(){} };
	},
	querySelector: function(sel) { return sel === '#jsa' ? __makeIframe() : null; },
	body: { appendChild: function(){}, removeChild: function(){} }
};
var window = globalThis;
window.__DDG_BE_VERSION__ = 1;
window.__DDG_FE_CHAT_HASH__ = 1;
window.top = window;
window.self = window;
window.document = document;
var __origKeys = Object.keys;
Object.keys = function(obj) {
	if (obj === window) return ['__DDG_BE_VERSION__', '__DDG_FE_CHAT_HASH__'];
	return __origKeys(obj);
};
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

	// Replace client_hashes[0] with Chrome UA
	chromeUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
	if len(scriptResult.ClientHashes) > 0 {
		scriptResult.ClientHashes[0] = chromeUA
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
