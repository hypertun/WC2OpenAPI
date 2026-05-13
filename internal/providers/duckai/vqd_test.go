package duckai

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestComputeVqdHash(t *testing.T) {
	// Create a simple VQD script that returns the required structure
	vqdScript := `
(async function() {
  return {
    server_hashes: ["hash1", "hash2", "hash3"],
    client_hashes: ["ua_here", "1234", "5678"],
    signals: {},
    meta: {
      v: "4",
      challenge_id: "test_challenge_id_vz95n",
      timestamp: "1234567890123",
      debug: ""
    }
  };
})()
`

	encoded := base64.StdEncoding.EncodeToString([]byte(vqdScript))

	client := &Client{}
	result, err := client.computeVqdHash(encoded)
	if err != nil {
		t.Fatalf("computeVqdHash failed: %v", err)
	}

	// Verify result is base64 encoded
	decoded, err := base64.StdEncoding.DecodeString(result)
	if err != nil {
		t.Fatalf("Failed to decode result: %v", err)
	}

	// Verify result is valid JSON
	var vqdResult vqdScriptResult
	err = json.Unmarshal(decoded, &vqdResult)
	if err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Verify structure
	if len(vqdResult.ServerHashes) != 3 {
		t.Errorf("Expected 3 server hashes, got %d", len(vqdResult.ServerHashes))
	}

	if vqdResult.Meta.ChallengeID != "test_challenge_id_vz95n" {
		t.Errorf("Expected challenge_id 'test_challenge_id_vz95n', got '%s'", vqdResult.Meta.ChallengeID)
	}

	if vqdResult.Meta.Timestamp != "1234567890123" {
		t.Errorf("Expected timestamp '1234567890123', got '%s'", vqdResult.Meta.Timestamp)
	}

	if vqdResult.Meta.V != "4" {
		t.Errorf("Expected v '4', got '%s'", vqdResult.Meta.V)
	}

	// Verify client_hashes[0] is the Chrome UA hash
	if len(vqdResult.ClientHashes) > 0 {
		// The hash should be SHA256 of the Chrome UA
		// Let's verify it's a valid base64 string of proper length (SHA256 is 32 bytes = 44 chars base64)
		if len(vqdResult.ClientHashes[0]) != 44 {
			t.Errorf("Expected client_hashes[0] to be base64 SHA256 (44 chars), got %d chars", len(vqdResult.ClientHashes[0]))
		}
	}

	t.Logf("VQD hash computation successful")
	t.Logf("Server hashes: %v", vqdResult.ServerHashes)
	t.Logf("Challenge ID: %s", vqdResult.Meta.ChallengeID)
	t.Logf("Timestamp: %s", vqdResult.Meta.Timestamp)
}

func TestComputeVqdHashWithDivFormat(t *testing.T) {
	// Simulate a newer script format with div-based hash2 computation
	vqdScript := `
(async function() {
  var div = document.createElement('div');
  div.innerHTML = '<span>test</span><div>content</div>';
  var elements = div.querySelectorAll('*');
  var hash2Value = String(237 + div.innerHTML.length * elements.length);
  
  return {
    server_hashes: ["hash1", "hash2", "hash3"],
    client_hashes: ["ua_input", hash2Value, "hash3"],
    signals: {},
    meta: {
      v: "4",
      challenge_id: "abc123def456ghi789jkl012mno345h8jbt",
      timestamp: "1234567890123",
      debug: ""
    }
  };
})()
`

	encoded := base64.StdEncoding.EncodeToString([]byte(vqdScript))

	client := &Client{}
	result, err := client.computeVqdHash(encoded)
	if err != nil {
		t.Fatalf("computeVqdHash failed: %v", err)
	}

	// Verify result is base64 encoded
	decoded, err := base64.StdEncoding.DecodeString(result)
	if err != nil {
		t.Fatalf("Failed to decode result: %v", err)
	}

	// Verify result is valid JSON with correct key order
	var vqdResult vqdScriptResult
	err = json.Unmarshal(decoded, &vqdResult)
	if err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Verify the script executed correctly
	if len(vqdResult.ServerHashes) != 3 {
		t.Errorf("Expected 3 server hashes, got %d", len(vqdResult.ServerHashes))
	}

	if vqdResult.Meta.ChallengeID != "abc123def456ghi789jkl012mno345h8jbt" {
		t.Errorf("Expected challenge_id, got '%s'", vqdResult.Meta.ChallengeID)
	}

	t.Logf("VQD hash computation with div format successful")
}

func TestComputeVqdHashJSONKeyOrder(t *testing.T) {
	// Verify JSON key ordering is preserved
	vqdScript := `
(async function() {
  return {
    server_hashes: ["h1"],
    client_hashes: ["ua", "c2"],
    signals: {},
    meta: {
      v: "4",
      challenge_id: "cid",
      timestamp: "ts",
      debug: "d"
    }
  };
})()
`

	encoded := base64.StdEncoding.EncodeToString([]byte(vqdScript))

	client := &Client{}
	result, err := client.computeVqdHash(encoded)
	if err != nil {
		t.Fatalf("computeVqdHash failed: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(result)
	if err != nil {
		t.Fatalf("Failed to decode result: %v", err)
	}

	// Parse and verify key order by checking JSON string directly
	jsonStr := string(decoded)
	
	// Expected order: server_hashes, client_hashes, signals, meta
	si := findKeyPosition(jsonStr, "server_hashes")
	ci := findKeyPosition(jsonStr, "client_hashes")
	sg := findKeyPosition(jsonStr, "signals")
	mi := findKeyPosition(jsonStr, "meta")

	if si > ci || ci > sg || sg > mi {
		t.Errorf("JSON keys not in correct order. server_hashes at %d, client_hashes at %d, signals at %d, meta at %d", si, ci, sg, mi)
		t.Logf("JSON: %s", jsonStr)
	}

	t.Logf("JSON key order is correct: server_hashes < client_hashes < signals < meta")
}

func findKeyPosition(jsonStr, key string) int {
	for i := 0; i < len(jsonStr)-len(key)-2; i++ {
		if jsonStr[i:i+len(key)+3] == `"`+key+`"` {
			return i
		}
	}
	return -1
}
