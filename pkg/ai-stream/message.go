package aistream

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
)

func (t Run) Text() string {
	var out strings.Builder
	for _, message := range t.Messages(false) {
		if message.Role != agui.RoleAssistant {
			continue
		}
		if content, _ := message.Content.(string); content != "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString(content)
		}
	}
	return out.String()
}

type projectedMessage struct {
	message   agui.Message
	content   strings.Builder
	toolCalls map[string]*agui.MessageToolCall
	toolOrder []string
}

type projectedToolCall struct {
	parent *projectedMessage
	call   *agui.MessageToolCall
	args   strings.Builder
}

func (t Run) Messages(includeReasoning bool) []agui.Message {
	if snapshot := latestMessagesSnapshot(t.Events, includeReasoning); len(snapshot) > 0 {
		return snapshot
	}

	byID := map[string]*projectedMessage{}
	var order []string
	toolCalls := map[string]*projectedToolCall{}
	currentTextMessageID := ""
	currentReasoningMessageID := ""
	openReasoningMessageID := ""
	openReasoningProjectedID := ""
	reasoningSegmentCounts := map[string]int{}

	ensureMessage := func(messageID, role string) *projectedMessage {
		if messageID == "" {
			messageID = t.MessageID
		}
		if role == "" {
			role = agui.RoleAssistant
		}
		message := byID[messageID]
		if message != nil {
			if message.message.Role == "" {
				message.message.Role = role
			}
			return message
		}
		message = &projectedMessage{
			message: agui.Message{ID: messageID, Role: role},
		}
		byID[messageID] = message
		order = append(order, messageID)
		return message
	}

	ensureToolCall := func(toolCallID string, evt agui.Event) *projectedToolCall {
		if toolCallID == "" {
			return nil
		}
		if tool := toolCalls[toolCallID]; tool != nil {
			return tool
		}
		parentID, _ := evt.Get("parentMessageId").(string)
		parent := ensureMessage(parentID, agui.RoleAssistant)
		call := &agui.MessageToolCall{
			ID:   toolCallID,
			Type: "function",
			Function: agui.ToolCallFunction{
				Name: firstString(evt.Get("toolName"), evt.Get("toolCallName")),
			},
		}
		if parent.toolCalls == nil {
			parent.toolCalls = map[string]*agui.MessageToolCall{}
		}
		parent.toolCalls[toolCallID] = call
		parent.toolOrder = append(parent.toolOrder, toolCallID)
		tool := &projectedToolCall{parent: parent, call: call}
		toolCalls[toolCallID] = tool
		return tool
	}
	ensureReasoningMessage := func(messageID string) *projectedMessage {
		if messageID == "" {
			messageID = currentReasoningMessageID
		}
		if messageID == "" {
			messageID = t.MessageID + "-reasoning"
		}
		currentReasoningMessageID = messageID
		if openReasoningMessageID != messageID || openReasoningProjectedID == "" {
			index := reasoningSegmentCounts[messageID]
			reasoningSegmentCounts[messageID] = index + 1
			openReasoningMessageID = messageID
			openReasoningProjectedID = messageID
			if index > 0 {
				openReasoningProjectedID = fmt.Sprintf("%s-segment-%d", messageID, index+1)
			}
		}
		return ensureMessage(openReasoningProjectedID, "reasoning")
	}
	closeReasoningMessage := func(messageID string) {
		if messageID == "" {
			messageID = currentReasoningMessageID
		}
		if messageID == "" {
			messageID = openReasoningMessageID
		}
		if messageID == "" || openReasoningMessageID == messageID {
			openReasoningMessageID = ""
			openReasoningProjectedID = ""
		}
	}

	for _, evt := range t.Events {
		eventType := evt.Type()
		if !isReasoningEventType(eventType) {
			closeReasoningMessage("")
		}
		switch eventType {
		case agui.EventTextMessageStart:
			messageID, _ := evt.Get("messageId").(string)
			role := firstString(evt.Get("role"), agui.RoleAssistant)
			ensureMessage(messageID, role)
			currentTextMessageID = messageID
		case agui.EventTextMessageContent:
			messageID, _ := evt.Get("messageId").(string)
			if messageID == "" {
				messageID = currentTextMessageID
			}
			message := ensureMessage(messageID, agui.RoleAssistant)
			message.content.WriteString(asString(evt.Get("delta")))
		case agui.EventTextMessageChunk:
			messageID, _ := evt.Get("messageId").(string)
			if messageID == "" {
				messageID = currentTextMessageID
			}
			if messageID == "" {
				messageID = t.MessageID
			}
			currentTextMessageID = messageID
			message := ensureMessage(messageID, firstString(evt.Get("role"), agui.RoleAssistant))
			message.content.WriteString(asString(evt.Get("delta")))
		case agui.EventReasoningMsgStart:
			if !includeReasoning {
				continue
			}
			messageID, _ := evt.Get("messageId").(string)
			ensureReasoningMessage(messageID)
		case agui.EventReasoningMsgCont:
			if !includeReasoning {
				continue
			}
			messageID, _ := evt.Get("messageId").(string)
			message := ensureReasoningMessage(messageID)
			message.content.WriteString(asString(evt.Get("delta")))
		case agui.EventReasoningMsgChunk:
			if !includeReasoning {
				continue
			}
			messageID, _ := evt.Get("messageId").(string)
			if delta := asString(evt.Get("delta")); delta != "" {
				message := ensureReasoningMessage(messageID)
				message.content.WriteString(delta)
			} else {
				closeReasoningMessage(messageID)
			}
		case agui.EventReasoningMsgEnd:
			messageID, _ := evt.Get("messageId").(string)
			closeReasoningMessage(messageID)
		case agui.EventReasoningEncrypted:
			entityID, _ := evt.Get("entityId").(string)
			encryptedValue, _ := evt.Get("encryptedValue").(string)
			switch evt.Get("subtype") {
			case "message":
				ensureReasoningMessage(entityID).message.EncryptedValue = encryptedValue
			case "tool-call":
				if tool := toolCalls[entityID]; tool != nil {
					tool.call.EncryptedValue = encryptedValue
				}
			}
		case agui.EventToolCallStart:
			toolCallID, _ := evt.Get("toolCallId").(string)
			tool := ensureToolCall(toolCallID, evt)
			if tool != nil && tool.call.Function.Name == "" {
				tool.call.Function.Name = firstString(evt.Get("toolName"), evt.Get("toolCallName"))
			}
		case agui.EventToolCallArgs:
			toolCallID, _ := evt.Get("toolCallId").(string)
			tool := ensureToolCall(toolCallID, evt)
			if tool == nil {
				continue
			}
			if delta, _ := evt.Get("delta").(string); delta != "" {
				tool.args.WriteString(delta)
				tool.call.Function.Arguments = tool.args.String()
			}
			if args := evt.Get("args"); args != nil {
				tool.call.Function.Arguments = asString(jsonString(args))
			}
		case agui.EventToolCallChunk:
			toolCallID, _ := evt.Get("toolCallId").(string)
			tool := ensureToolCall(toolCallID, evt)
			if tool == nil {
				continue
			}
			if tool.call.Function.Name == "" {
				tool.call.Function.Name = firstString(evt.Get("toolName"), evt.Get("toolCallName"))
			}
			if delta, _ := evt.Get("delta").(string); delta != "" {
				tool.args.WriteString(delta)
				tool.call.Function.Arguments = tool.args.String()
			}
		case agui.EventToolCallEnd:
			toolCallID, _ := evt.Get("toolCallId").(string)
			tool := ensureToolCall(toolCallID, evt)
			if tool != nil && tool.call.Function.Name == "" {
				tool.call.Function.Name = firstString(evt.Get("toolName"), evt.Get("toolCallName"))
			}
		case agui.EventToolCallResult:
			messageID, _ := evt.Get("messageId").(string)
			toolCallID, _ := evt.Get("toolCallId").(string)
			message := ensureMessage(messageID, firstString(evt.Get("role"), agui.RoleTool))
			message.message.ToolCallID = toolCallID
			message.content.WriteString(asString(evt.Get("content")))
			if state, _ := evt.Get("state").(string); state == agui.ToolResultStateError {
				message.message.Error = asString(evt.Get("error"))
			}
		case agui.EventActivitySnapshot:
			messageID, _ := evt.Get("messageId").(string)
			message := ensureMessage(messageID, "activity")
			message.message.ActivityType = firstString(evt.Get("activityType"))
			if content, ok := evt.Get("content").(map[string]any); ok {
				message.message.Content = content
			}
		}
	}

	out := make([]agui.Message, 0, len(order))
	for _, messageID := range order {
		state := byID[messageID]
		if state == nil {
			continue
		}
		message := state.message
		if state.content.Len() > 0 {
			message.Content = state.content.String()
		}
		if len(state.toolOrder) > 0 {
			message.ToolCalls = make([]agui.MessageToolCall, 0, len(state.toolOrder))
			for _, toolCallID := range state.toolOrder {
				call := state.toolCalls[toolCallID]
				if call != nil {
					message.ToolCalls = append(message.ToolCalls, *call)
				}
			}
		}
		if message.Role == "reasoning" && !includeReasoning {
			continue
		}
		if message.Content == nil && len(message.ToolCalls) == 0 && message.ToolCallID == "" && message.EncryptedValue == "" && message.ActivityType == "" {
			continue
		}
		out = append(out, message)
	}
	return out
}

func latestMessagesSnapshot(events []agui.Event, includeReasoning bool) []agui.Message {
	var snapshot []agui.Message
	for _, evt := range events {
		if evt.Type() != agui.EventMessagesSnapshot {
			continue
		}
		messages, ok := evt.Get("messages").([]agui.Message)
		if ok {
			snapshot = messages
			continue
		}
		if decoded := decodeMessagesSnapshot(evt.Get("messages")); len(decoded) > 0 {
			snapshot = decoded
		}
	}
	if len(snapshot) == 0 {
		return nil
	}
	out := make([]agui.Message, 0, len(snapshot))
	for _, message := range snapshot {
		if message.Role == "reasoning" && !includeReasoning {
			continue
		}
		out = append(out, message)
	}
	return out
}

func decodeMessagesSnapshot(value any) []agui.Message {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]agui.Message, 0, len(raw))
	for _, item := range raw {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil
		}
		var message agui.Message
		if err := json.Unmarshal(encoded, &message); err != nil {
			return nil
		}
		out = append(out, message)
	}
	return out
}

type projectedPart struct {
	part    MessagePart
	content strings.Builder
}

func (t Run) FinalBeeperAIMessage(textBudget int, includeThinking bool) UIMessage {
	message := UIMessage{
		ID:   t.MessageID,
		Role: agui.RoleAssistant,
	}
	textParts := map[string]*projectedPart{}
	thinkingParts := map[string]*projectedPart{}
	toolParts := map[string]MessagePart{}
	currentTextMessageID := ""
	openTextMessageID := ""
	openTextPartID := ""
	currentReasoningMessageID := ""
	openThinkingMessageID := ""
	openThinkingPartID := ""
	textSegmentCounts := map[string]int{}
	thinkingSegmentCounts := map[string]int{}
	appendPart := func(part MessagePart) MessagePart {
		message.Parts = append(message.Parts, part)
		return part
	}
	ensureTextPart := func(messageID string) *projectedPart {
		if messageID == "" {
			messageID = currentTextMessageID
		}
		if messageID == "" {
			messageID = t.MessageID
		}
		currentTextMessageID = messageID
		if openTextMessageID != messageID || openTextPartID == "" {
			index := textSegmentCounts[messageID]
			textSegmentCounts[messageID] = index + 1
			openTextMessageID = messageID
			openTextPartID = messageID
			if index > 0 {
				openTextPartID = fmt.Sprintf("%s-segment-%d", messageID, index+1)
			}
		}
		if part := textParts[openTextPartID]; part != nil {
			return part
		}
		part := appendPart(MessagePart{"type": "text", "id": openTextPartID, "messageId": messageID, "content": "", "state": agui.PartStateStreaming})
		projected := &projectedPart{part: part}
		textParts[openTextPartID] = projected
		return projected
	}
	closeTextPart := func(messageID string) {
		if messageID == "" {
			messageID = currentTextMessageID
		}
		if messageID == "" {
			messageID = openTextMessageID
		}
		if messageID != "" && openTextMessageID != messageID {
			return
		}
		if part := textParts[openTextPartID]; part != nil {
			part.part["state"] = agui.PartStateDone
		}
		openTextMessageID = ""
		openTextPartID = ""
	}
	ensureThinkingPart := func(messageID string) *projectedPart {
		if messageID == "" {
			messageID = currentReasoningMessageID
		}
		if messageID == "" {
			messageID = t.MessageID + "-reasoning"
		}
		currentReasoningMessageID = messageID
		if openThinkingMessageID != messageID || openThinkingPartID == "" {
			index := thinkingSegmentCounts[messageID]
			thinkingSegmentCounts[messageID] = index + 1
			openThinkingMessageID = messageID
			openThinkingPartID = messageID
			if index > 0 {
				openThinkingPartID = fmt.Sprintf("%s-segment-%d", messageID, index+1)
			}
		}
		if part := thinkingParts[openThinkingPartID]; part != nil {
			return part
		}
		part := appendPart(MessagePart{"type": "thinking", "id": openThinkingPartID, "messageId": messageID, "content": "", "state": agui.PartStateStreaming})
		projected := &projectedPart{part: part}
		thinkingParts[openThinkingPartID] = projected
		return projected
	}
	closeThinkingPart := func(messageID string) {
		if messageID == "" {
			messageID = currentReasoningMessageID
		}
		if messageID == "" {
			messageID = openThinkingMessageID
		}
		if messageID != "" && openThinkingMessageID != messageID {
			return
		}
		if part := thinkingParts[openThinkingPartID]; part != nil {
			part.part["state"] = agui.PartStateDone
		}
		openThinkingMessageID = ""
		openThinkingPartID = ""
	}
	for _, evt := range t.Events {
		eventType := evt.Type()
		if !isReasoningEventType(eventType) {
			closeThinkingPart("")
		}
		if isActivityEventType(eventType) {
			closeTextPart("")
		}
		switch eventType {
		case agui.EventTextMessageStart:
			messageID, _ := evt.Get("messageId").(string)
			currentTextMessageID = messageID
		case agui.EventTextMessageContent:
			delta, _ := evt.Get("delta").(string)
			if delta == "" {
				continue
			}
			messageID, _ := evt.Get("messageId").(string)
			if messageID == "" {
				messageID = currentTextMessageID
			}
			ensureTextPart(messageID).content.WriteString(delta)
		case agui.EventTextMessageChunk:
			messageID, _ := evt.Get("messageId").(string)
			if messageID == "" {
				messageID = currentTextMessageID
			}
			currentTextMessageID = messageID
			if delta, _ := evt.Get("delta").(string); delta != "" {
				ensureTextPart(messageID).content.WriteString(delta)
			}
		case agui.EventTextMessageEnd:
			messageID, _ := evt.Get("messageId").(string)
			closeTextPart(messageID)
		case agui.EventReasoningMsgStart:
			if !includeThinking {
				continue
			}
			messageID, _ := evt.Get("messageId").(string)
			ensureThinkingPart(messageID)
		case agui.EventReasoningMsgCont:
			delta, _ := evt.Get("delta").(string)
			if delta == "" {
				continue
			}
			if !includeThinking {
				continue
			}
			messageID, _ := evt.Get("messageId").(string)
			ensureThinkingPart(messageID).content.WriteString(delta)
		case agui.EventReasoningMsgChunk:
			if !includeThinking {
				continue
			}
			messageID, _ := evt.Get("messageId").(string)
			if delta, _ := evt.Get("delta").(string); delta != "" {
				ensureThinkingPart(messageID).content.WriteString(delta)
			} else {
				closeThinkingPart(messageID)
			}
		case agui.EventReasoningMsgEnd:
			messageID, _ := evt.Get("messageId").(string)
			closeThinkingPart(messageID)
		case agui.EventToolCallStart:
			toolCallID, _ := evt.Get("toolCallId").(string)
			if toolCallID == "" {
				continue
			}
			part := MessagePart{
				"type":       "tool-call",
				"id":         toolCallID,
				"toolCallId": toolCallID,
				"name":       firstString(evt.Get("toolName"), evt.Get("toolCallName")),
				"arguments":  "",
				"state":      firstString(evt.Get("state")),
			}
			if index := evt.Get("index"); index != nil {
				part["index"] = index
			}
			if metadata := evt.Get("metadata"); metadata != nil {
				part["metadata"] = metadata
			}
			toolParts[toolCallID] = appendPart(part)
		case agui.EventToolCallArgs:
			toolCallID, _ := evt.Get("toolCallId").(string)
			part := toolParts[toolCallID]
			if part == nil {
				part = appendPart(MessagePart{"type": "tool-call", "id": toolCallID, "toolCallId": toolCallID, "arguments": ""})
				toolParts[toolCallID] = part
			}
			part["state"] = firstString(evt.Get("state"))
			if delta, _ := evt.Get("delta").(string); delta != "" {
				part["arguments"] = asString(part["arguments"]) + delta
			}
			if args := evt.Get("args"); args != nil {
				part["input"] = args
			}
		case agui.EventToolCallEnd:
			toolCallID, _ := evt.Get("toolCallId").(string)
			part := toolParts[toolCallID]
			if part == nil {
				part = appendPart(MessagePart{"type": "tool-call", "id": toolCallID, "toolCallId": toolCallID})
				toolParts[toolCallID] = part
			}
			part["name"] = firstString(part["name"], evt.Get("toolName"), evt.Get("toolCallName"))
			part["state"] = firstString(evt.Get("state"))
			if input := evt.Get("input"); input != nil {
				part["input"] = input
			}
			if result := evt.Get("result"); result != nil {
				part["output"] = jsonValue(result)
			}
		case agui.EventToolCallResult:
			toolCallID, _ := evt.Get("toolCallId").(string)
			if toolCallID == "" {
				continue
			}
			part := toolParts[toolCallID]
			if part == nil {
				part = appendPart(MessagePart{"type": "tool-call", "id": toolCallID, "toolCallId": toolCallID})
				toolParts[toolCallID] = part
			}
			content := asString(evt.Get("content"))
			if previous, ok := part["output"].(string); ok && previous != "" {
				content = previous + content
			}
			if content != "" {
				output := toolResultOutput(content, firstString(evt.Get("state")), evt.Get("error"))
				part["output"] = output
				if result, ok := ParseApprovalToolResult(output); ok {
					part["approvalResponse"] = result
					part["state"] = ToolStateApprovalResponded
				} else {
					part["state"] = agui.ToolStateInputComplete
				}
			} else {
				part["state"] = agui.ToolStateInputComplete
			}
		case agui.EventRunFinished:
			for _, interrupt := range runFinishedInterrupts(evt.Get("outcome")) {
				if interrupt.Reason != agui.InterruptReasonToolCall || interrupt.ToolCallID == "" {
					continue
				}
				part := toolParts[interrupt.ToolCallID]
				if part == nil {
					part = appendPart(MessagePart{"type": "tool-call", "id": interrupt.ToolCallID, "toolCallId": interrupt.ToolCallID})
					toolParts[interrupt.ToolCallID] = part
				}
				part["state"] = ToolStateApprovalRequested
				if metadata := interrupt.Metadata; metadata != nil {
					if toolName := firstString(metadata["toolName"]); toolName != "" {
						part["name"] = firstString(part["name"], toolName)
					}
					if input, ok := metadata["input"]; ok {
						part["input"] = input
					}
					if approval, ok := metadata["approval"]; ok {
						part["approval"] = approval
					} else {
						part["approval"] = ToolApproval{ID: interrupt.ID, NeedsApproval: true}
					}
				} else {
					part["approval"] = ToolApproval{ID: interrupt.ID, NeedsApproval: true}
				}
			}
		case agui.EventCustom:
			name, _ := evt.Get("name").(string)
			value, _ := evt.Get("value").(map[string]any)
			switch name {
			case "com.beeper.source":
				part := cloneValueMap(value)
				part["type"] = "source-url"
				if asString(part["sourceId"]) == "" {
					part["sourceId"] = firstString(part["url"], part["title"])
				}
				message.Parts = append(message.Parts, part)
			case "com.beeper.document":
				part := cloneValueMap(value)
				part["type"] = "source-document"
				if asString(part["sourceId"]) == "" {
					part["sourceId"] = firstString(part["id"], part["title"])
				}
				message.Parts = append(message.Parts, part)
			case "com.beeper.file":
				part := cloneValueMap(value)
				part["type"] = "file"
				message.Parts = append(message.Parts, part)
			case "com.beeper.data":
				message.Parts = append(message.Parts, MessagePart{"type": "data-com-beeper-data", "data": value})
			}
		}
	}
	if t.Status.State != "" && t.Status.State != "streaming" {
		closeTextPart("")
		closeThinkingPart("")
	}
	if t.Status.State != "" && t.Status.State != "streaming" {
		for _, part := range toolParts {
			finalizeOpenToolPart(part, t.Status.State)
		}
	}
	for _, projected := range textParts {
		projected.part["content"] = projected.content.String()
		compactTextPart(projected.part, textBudget)
	}
	for _, projected := range thinkingParts {
		projected.part["content"] = projected.content.String()
		compactTextPart(projected.part, textBudget)
	}
	return message
}

func toolResultOutput(content string, state string, err any) any {
	output := jsonValue(content)
	result, ok := output.(map[string]any)
	if !ok {
		if state == agui.ToolResultStateError {
			reason := asString(err)
			if reason == "" {
				reason = content
			}
			return map[string]any{
				"state":  agui.ToolResultStateError,
				"status": "failed",
				"reason": reason,
			}
		}
		return output
	}
	if state == agui.ToolResultStateError {
		if result["state"] == nil {
			result["state"] = agui.ToolResultStateError
		}
		if result["status"] == nil {
			result["status"] = "failed"
		}
		if result["reason"] == nil && err != nil {
			result["reason"] = asString(err)
		}
	} else if state == agui.ToolResultStateComplete {
		if result["state"] == nil {
			result["state"] = agui.ToolResultStateComplete
		}
		if result["status"] == nil {
			result["status"] = "success"
		}
	}
	return result
}

func finalizeOpenToolPart(part MessagePart, runState string) {
	if part == nil {
		return
	}
	if _, hasOutput := part["output"]; hasOutput {
		return
	}
	state, _ := part["state"].(string)
	switch state {
	case ToolStateApprovalRequested, ToolStateApprovalResponded:
		return
	}
	reason := "run finalized before tool completed"
	if runState == "aborted" {
		reason = "run aborted before tool completed"
	} else if runState == "error" {
		reason = "run failed before tool completed"
	}
	part["state"] = agui.ToolStateInputComplete
	part["output"] = map[string]any{
		"state":  agui.ToolResultStateError,
		"status": "failed",
		"reason": reason,
	}
}

func runFinishedInterrupts(value any) []agui.Interrupt {
	switch outcome := value.(type) {
	case agui.RunFinishedOutcome:
		if outcome.Type != agui.OutcomeInterrupt {
			return nil
		}
		return outcome.Interrupts
	case *agui.RunFinishedOutcome:
		if outcome == nil {
			return nil
		}
		return runFinishedInterrupts(*outcome)
	case map[string]any:
		if outcome["type"] != agui.OutcomeInterrupt {
			return nil
		}
		switch rawInterrupts := outcome["interrupts"].(type) {
		case []agui.Interrupt:
			return rawInterrupts
		case []any:
			interrupts := make([]agui.Interrupt, 0, len(rawInterrupts))
			for _, raw := range rawInterrupts {
				switch interrupt := raw.(type) {
				case agui.Interrupt:
					interrupts = append(interrupts, interrupt)
				case map[string]any:
					metadata, _ := interrupt["metadata"].(map[string]any)
					responseSchema, _ := interrupt["responseSchema"].(map[string]any)
					interrupts = append(interrupts, agui.Interrupt{
						ID:             firstString(interrupt["id"]),
						Reason:         firstString(interrupt["reason"]),
						Message:        firstString(interrupt["message"]),
						ToolCallID:     firstString(interrupt["toolCallId"]),
						ResponseSchema: responseSchema,
						ExpiresAt:      firstString(interrupt["expiresAt"]),
						Metadata:       metadata,
					})
				}
			}
			return interrupts
		}
	}
	return nil
}

func isReasoningEventType(eventType string) bool {
	switch eventType {
	case agui.EventReasoningStart,
		agui.EventReasoningEnd,
		agui.EventReasoningMsgStart,
		agui.EventReasoningMsgCont,
		agui.EventReasoningMsgEnd,
		agui.EventReasoningMsgChunk,
		agui.EventReasoningEncrypted:
		return true
	default:
		return false
	}
}

func isActivityEventType(eventType string) bool {
	if isReasoningEventType(eventType) {
		return true
	}
	switch eventType {
	case agui.EventToolCallStart,
		agui.EventToolCallArgs,
		agui.EventToolCallEnd,
		agui.EventToolCallChunk,
		agui.EventToolCallResult:
		return true
	default:
		return false
	}
}

func (t Run) InitialBeeperAIMessage() UIMessage {
	message := UIMessage{
		ID:   t.MessageID,
		Role: agui.RoleAssistant,
	}
	if t.Preview.Text != "" {
		message.Parts = []MessagePart{{
			"type":    "text",
			"content": t.Preview.Text,
			"state":   agui.PartStateStreaming,
		}}
	} else {
		message.Parts = []MessagePart{}
	}
	return message
}

func compactTextPart(part MessagePart, budget int) {
	if part == nil {
		return
	}
	content, _ := part["content"].(string)
	if budget <= 0 {
		if part["state"] == "" {
			part["state"] = agui.PartStateDone
		}
		return
	}
	preview := BoundedPreview(content, budget)
	part["content"] = preview
	if len(preview) < len(content) {
		part["providerMetadata"] = map[string]any{"truncated": true}
	}
	if part["state"] == "" {
		part["state"] = agui.PartStateDone
	}
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func cloneValueMap(value map[string]any) MessagePart {
	cp := make(MessagePart, len(value)+1)
	for key, item := range value {
		cp[key] = item
	}
	return cp
}

func firstString(values ...any) string {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
	}
	return ""
}
