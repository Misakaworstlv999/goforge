package llm

import "testing"

func TestSystemMessage(t *testing.T) {
	m := SystemMessage("you are helpful")
	if m.Role != RoleSystem {
		t.Errorf("got role %q, want %q", m.Role, RoleSystem)
	}
	if m.Content != "you are helpful" {
		t.Errorf("got content %q, want %q", m.Content, "you are helpful")
	}
}

func TestUserMessage(t *testing.T) {
	m := UserMessage("hello")
	if m.Role != RoleUser {
		t.Errorf("got role %q, want %q", m.Role, RoleUser)
	}
	if m.Content != "hello" {
		t.Errorf("got content %q, want %q", m.Content, "hello")
	}
}

func TestAssistantMessage(t *testing.T) {
	m := AssistantMessage("hi there")
	if m.Role != RoleAssistant {
		t.Errorf("got role %q, want %q", m.Role, RoleAssistant)
	}
	if m.Content != "hi there" {
		t.Errorf("got content %q, want %q", m.Content, "hi there")
	}
}

func TestToolMessage(t *testing.T) {
	m := ToolMessage("call_123", `{"result": 42}`, false)
	if m.Role != RoleTool {
		t.Errorf("got role %q, want %q", m.Role, RoleTool)
	}
	if m.ToolCallID != "call_123" {
		t.Errorf("got tool_call_id %q, want %q", m.ToolCallID, "call_123")
	}
	if m.Content != `{"result": 42}` {
		t.Errorf("got content %q", m.Content)
	}
}

func TestRoleConstants(t *testing.T) {
	tests := []struct {
		role Role
		want string
	}{
		{RoleSystem, "system"},
		{RoleUser, "user"},
		{RoleAssistant, "assistant"},
		{RoleTool, "tool"},
	}
	for _, tt := range tests {
		if string(tt.role) != tt.want {
			t.Errorf("Role %v: got %q, want %q", tt.role, string(tt.role), tt.want)
		}
	}
}
