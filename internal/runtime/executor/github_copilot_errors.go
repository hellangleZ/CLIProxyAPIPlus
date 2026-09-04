package executor

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	copilotSolContextWindowExceededMessage = "Your input exceeds the context window of this model. Please adjust your input and try again."
	copilotGeminiInvalidRequestMessage     = "invalid request body"
	copilotPayloadTooLargeMessage          = "Request Entity Too Large"
)

func supportsCopilotPromptLimitNormalization(model string) bool {
	return isGrokClaudeBridgeModel(model) || isGPT56SolClaudeBridgeModel(model) || isGemini38ClaudeBridgeModel(model)
}

// newGitHubCopilotStatusErr preserves upstream errors except for exact known
// prompt-limit responses from selected bridge models. Claude Code recognizes
// the normalized message and can start its reactive compact flow.
func newGitHubCopilotStatusErr(statusCode int, body []byte, model string) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusRequestEntityTooLarge && isGemini38ClaudeBridgeModel(model) && strings.TrimSpace(string(body)) == copilotPayloadTooLargeMessage {
		err.code = http.StatusBadRequest
		err.msg = "prompt is too long"
		return err
	}
	if statusCode != http.StatusBadRequest || !supportsCopilotPromptLimitNormalization(model) || !gjson.ValidBytes(body) {
		return err
	}

	outerCode := gjson.GetBytes(body, "error.code")
	outerMessage := gjson.GetBytes(body, "error.message")
	if outerCode.Type != gjson.String || outerCode.String() != "invalid_request_body" || outerMessage.Type != gjson.String {
		return err
	}
	if isGPT56SolClaudeBridgeModel(model) && outerMessage.String() == copilotSolContextWindowExceededMessage {
		err.msg = "prompt is too long"
		return err
	}
	if isGemini38ClaudeBridgeModel(model) && outerMessage.String() == copilotGeminiInvalidRequestMessage {
		err.msg = "prompt is too long"
		return err
	}

	nestedBody := []byte(outerMessage.String())
	if !gjson.ValidBytes(nestedBody) {
		return err
	}
	innerCode := gjson.GetBytes(nestedBody, "code")
	innerMessage := gjson.GetBytes(nestedBody, "error")
	if innerCode.Type != gjson.String || innerCode.String() != "invalid-argument" || innerMessage.Type != gjson.String {
		return err
	}

	message := innerMessage.String()
	var limit, actual int64
	matched, scanErr := fmt.Sscanf(message, "This model's maximum prompt length is %d but the request contains %d tokens.", &limit, &actual)
	if scanErr != nil || matched != 2 || actual <= limit {
		return err
	}
	expected := fmt.Sprintf("This model's maximum prompt length is %d but the request contains %d tokens.", limit, actual)
	if message != expected {
		return err
	}

	err.msg = fmt.Sprintf("prompt is too long: %d tokens > %d", actual, limit)
	return err
}
