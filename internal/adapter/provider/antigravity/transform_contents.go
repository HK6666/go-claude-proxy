package antigravity

import (
	"encoding/json"
	"log"
	"strings"
)

// buildContents converts Claude messages to Gemini contents
// Reference: Antigravity-Manager's build_contents
func buildContents(
	messages []ClaudeMessage,
	mappedModel string,
	sessionID string,
	signatureCache *SignatureCache,
) ([]map[string]interface{}, error) {
	contents := []map[string]interface{}{}

	// State shared across the full conversation (matches Antigravity-Manager)
	toolIDToName := make(map[string]string)
	lastThoughtSignature := ""

	for _, msg := range messages {
		parts := []map[string]interface{}{}

		// String style content: trim and ignore "(no content)" (matches Antigravity-Manager)
		if text, ok := msg.Content.(string); ok {
			if text != "(no content)" {
				trimmed := strings.TrimSpace(text)
				if trimmed != "" {
					parts = append(parts, map[string]interface{}{"text": trimmed})
				}
			}
		} else {
			// Array style content blocks
			contentBlocks := parseContentBlocks(msg.Content)
			for _, block := range contentBlocks {
				switch block.Type {
				case "thinking":
					part := processThinkingBlock(block, &parts, mappedModel, lastThoughtSignature, signatureCache)
					if part != nil {
						parts = append(parts, part)
						if sig, ok := part["thoughtSignature"].(string); ok && sig != "" {
							lastThoughtSignature = sig
						}
					}

				case "redacted_thinking":
					parts = append(parts, map[string]interface{}{
						"text": "[Redacted Thinking: " + block.Data + "]",
					})

				case "text":
					if block.Text == "(no content)" {
						continue
					}
					parts = append(parts, map[string]interface{}{
						"text": block.Text,
					})

				case "tool_use":
					part := processToolUseBlock(block, lastThoughtSignature, signatureCache)
					parts = append(parts, part)
					toolIDToName[block.ID] = block.Name

				case "tool_result":
					part := processToolResultBlock(block, toolIDToName, lastThoughtSignature)
					parts = append(parts, part)

				case "image":
					if part := processInlineDataBlock(block); part != nil {
						parts = append(parts, part)
					}
				case "document":
					if part := processInlineDataBlock(block); part != nil {
						parts = append(parts, part)
					}
				}
			}
		}

		// Build content
		if len(parts) == 0 {
			continue
		}
		role := mapRole(msg.Role)
		contents = append(contents, map[string]interface{}{
			"role":  role,
			"parts": parts,
		})
	}

	// Merge adjacent same roles
	contents = mergeAdjacentRoles(contents)

	return contents, nil
}

// processThinkingBlock handles Thinking blocks with position and compatibility checks
// Reference: Antigravity-Manager's Thinking block processing
func processThinkingBlock(
	block ContentBlock,
	parts *[]map[string]interface{},
	mappedModel string,
	lastThoughtSignature string,
	signatureCache *SignatureCache,
) map[string]interface{} {
	// 1. Position check: must be first block
	if len(*parts) > 0 {
		log.Println("[Antigravity] Thinking block not first, downgrade to text")
		return map[string]interface{}{
			"text": block.Thinking,
		}
	}

	// 2. Empty block check
	if block.Thinking == "" {
		return map[string]interface{}{
			"text": "...",
		}
	}

	// 3. Build Thinking Part
	part := map[string]interface{}{
		"text":    block.Thinking,
		"thought": true,
	}

	// 4. Signature handling
	signature := block.Signature
	if signature == "" && lastThoughtSignature != "" {
		signature = lastThoughtSignature
	}

	// 5. Signature compatibility check
	if signature != "" && signatureCache != nil {
		if cachedFamily := signatureCache.GetSignatureFamily(signature); cachedFamily != "" {
			if !IsModelCompatible(cachedFamily, mappedModel) {
				log.Printf("[Antigravity] Incompatible signature detected (Family: %s, Target: %s). Dropping signature.", cachedFamily, mappedModel)
				return map[string]interface{}{"text": block.Thinking}
			}
		}
	}

	// Valid signature (thinking threshold)
	if hasValidThinkingSignature(block.Thinking, signature) {
		part["thoughtSignature"] = signature
	}

	return part
}

// processToolUseBlock handles ToolUse blocks with signature recovery
// Reference: Antigravity-Manager's ToolUse processing
func processToolUseBlock(
	block ContentBlock,
	lastThoughtSignature string,
	signatureCache *SignatureCache,
) map[string]interface{} {
	// Clean args to remove JSON Schema fields that Gemini doesn't support
	// Reference: Antigravity-Manager's clean_json_schema call after building functionCall
	var cleanedArgs map[string]interface{}
	if block.Input != nil {
		// Deep copy to avoid modifying original
		cleanedArgs = deepCopyMapForArgs(block.Input)
		CleanJSONSchema(cleanedArgs)
	}

	part := map[string]interface{}{
		"functionCall": map[string]interface{}{
			"name": block.Name,
			"args": cleanedArgs,
			"id":   block.ID,
		},
	}

	// Signature recovery priority (4 layers):
	// 1. Client-provided signature
	// 2. Context signature (last_thought_signature)
	// 3. Cached signature (from previous tool calls)
	// 4. Global fallback signature (from cache)
	// Reference: Antigravity-Manager's multi-layer signature recovery
	signature := block.Signature
	if signature == "" && lastThoughtSignature != "" {
		signature = lastThoughtSignature
	}
	if signature == "" && signatureCache != nil {
		signature = signatureCache.GetToolSignature(block.ID)
	}
	if signature == "" {
		// Final fallback: global signature store (best-effort)
		signature = GetThoughtSignature()
	}

	if signature != "" {
		part["thoughtSignature"] = signature
	}

	return part
}

// processToolResultBlock handles ToolResult blocks with empty result injection
// Reference: Antigravity-Manager's ToolResult processing
func processToolResultBlock(
	block ContentBlock,
	toolIDToName map[string]string,
	lastThoughtSignature string,
) map[string]interface{} {
	// 1. Merge content
	mergedContent := extractToolResultContent(block.Content)

	// 2. Empty result injection
	if strings.TrimSpace(mergedContent) == "" {
		if block.IsError != nil && *block.IsError {
			mergedContent = "Tool execution failed with no output."
		} else {
			mergedContent = "Command executed successfully."
		}
	}

	// 3. Get tool name
	toolName := toolIDToName[block.ToolUseID]
	if toolName == "" {
		toolName = block.ToolUseID
	}

	part := map[string]interface{}{
		"functionResponse": map[string]interface{}{
			"name": toolName,
			"response": map[string]interface{}{
				"result": mergedContent,
			},
			"id": block.ToolUseID,
		},
	}

	// Backfill signature from context
	// Reference: Antigravity-Manager's tool result signature backfill
	if lastThoughtSignature != "" {
		part["thoughtSignature"] = lastThoughtSignature
	}

	return part
}

// processImageBlock handles image blocks
func processInlineDataBlock(block ContentBlock) map[string]interface{} {
	if block.Source == nil || block.Source.Type != "base64" {
		return nil
	}
	return map[string]interface{}{
		"inlineData": map[string]interface{}{
			"mimeType": block.Source.MediaType,
			"data":     block.Source.Data,
		},
	}
}

// extractToolResultContent extracts text content from tool_result
func extractToolResultContent(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		var texts []string
		for _, item := range c {
			if blockMap, ok := item.(map[string]interface{}); ok {
				if text, ok := blockMap["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		// Try to serialize as JSON
		if data, err := json.Marshal(content); err == nil {
			return string(data)
		}
		return ""
	}
}

// mapRole converts Claude role to Gemini role
func mapRole(claudeRole string) string {
	switch claudeRole {
	case "user":
		return "user"
	case "assistant":
		return "model"
	default:
		return claudeRole
	}
}

// mergeAdjacentRoles merges adjacent contents with same role
// Gemini API strictly requires alternating user/model roles
func mergeAdjacentRoles(contents []map[string]interface{}) []map[string]interface{} {
	if len(contents) <= 1 {
		return contents
	}

	merged := []map[string]interface{}{contents[0]}

	for i := 1; i < len(contents); i++ {
		lastRole := merged[len(merged)-1]["role"].(string)
		currRole := contents[i]["role"].(string)

		if lastRole == currRole {
			// Merge parts
			lastParts, _ := merged[len(merged)-1]["parts"].([]map[string]interface{})
			currParts, _ := contents[i]["parts"].([]map[string]interface{})
			merged[len(merged)-1]["parts"] = append(lastParts, currParts...)
		} else {
			merged = append(merged, contents[i])
		}
	}

	return merged
}

// deepCopyMapForArgs creates a deep copy of a map for args cleaning
func deepCopyMapForArgs(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}

	dst := make(map[string]interface{}, len(src))

	for key, value := range src {
		switch v := value.(type) {
		case map[string]interface{}:
			dst[key] = deepCopyMapForArgs(v)
		case []interface{}:
			dst[key] = deepCopySliceForArgs(v)
		default:
			dst[key] = v
		}
	}

	return dst
}

// deepCopySliceForArgs creates a deep copy of a slice for args cleaning
func deepCopySliceForArgs(src []interface{}) []interface{} {
	if src == nil {
		return nil
	}

	dst := make([]interface{}, len(src))

	for i, value := range src {
		switch v := value.(type) {
		case map[string]interface{}:
			dst[i] = deepCopyMapForArgs(v)
		case []interface{}:
			dst[i] = deepCopySliceForArgs(v)
		default:
			dst[i] = v
		}
	}

	return dst
}
