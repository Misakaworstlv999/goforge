package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/llm/anthropic"
	"github.com/Misakaworstlv999/goforge/pkg/llm/openai"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/tool/builtin"
)

func main() {
	provider := flag.String("provider", "openai", "LLM provider: openai or anthropic")
	model := flag.String("model", "", "Model name (e.g., gpt-4o, claude-sonnet-4-20250514)")
	baseURL := flag.String("base-url", "", "Custom API base URL")
	apiKey := flag.String("api-key", "", "API key (defaults to OPENAI_API_KEY or ANTHROPIC_API_KEY env var)")
	system := flag.String("system", "You are a helpful assistant. You can use tools when needed.", "System prompt")
	useTools := flag.Bool("tools", false, "Enable single-step tool calling mode (calculator + clock)")
	useAgent := flag.Bool("agent", false, "Enable ReAct agent mode: multi-step think-act-observe loop")
	maxSteps := flag.Int("max-steps", 10, "Max ReAct steps (agent mode only)")
	flag.Parse()

	client := createClient(*provider, *model, *baseURL, *apiKey)

	// Agent mode runs the full ReAct loop; it supersedes single-step tool mode.
	if *useAgent {
		runAgentMode(client, *system, *maxSteps)
		return
	}

	var registry *tool.Registry
	if *useTools {
		registry = tool.NewRegistry()
		_ = registry.Register(builtin.NewCalculator(), builtin.NewClock())
		fmt.Println("GoForge Chat — Tool Calling Mode (type 'exit' to quit)")
		fmt.Println("Available tools: calculator, current_time")
	} else {
		fmt.Println("GoForge Chat (type 'exit' to quit)")
	}
	fmt.Println("---")

	messages := []llm.Message{llm.SystemMessage(*system)}
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" {
			break
		}

		messages = append(messages, llm.UserMessage(input))

		if *useTools && registry != nil {
			handleToolChat(client, registry, &messages)
		} else {
			handleStreamChat(client, &messages)
		}
	}
}

// runAgentMode drives the ReAct agent: each line of input is run as a task to
// completion, streaming Think/ToolCall/ToolResult/Response events as they occur.
func runAgentMode(client llm.LLM, system string, maxSteps int) {
	registry := tool.NewRegistry()
	_ = registry.Register(builtin.NewCalculator(), builtin.NewClock())

	a := agent.New(client, registry,
		agent.WithSystemPrompt(system),
		agent.WithMaxSteps(maxSteps),
		agent.WithToolTimeout(30*time.Second),
	)

	fmt.Println("GoForge Chat — ReAct Agent Mode (type 'exit' to quit)")
	fmt.Printf("Available tools: calculator, current_time | max steps: %d\n", maxSteps)
	fmt.Println("---")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" {
			break
		}
		handleAgentChat(a, input)
	}
}

// handleAgentChat runs one task through the agent and renders its event stream.
func handleAgentChat(a agent.Agent, task string) {
	for ev, err := range a.Run(context.Background(), task) {
		switch ev.Type {
		case agent.EventThink:
			if ev.Content != "" {
				fmt.Printf("[think] %s\n", ev.Content)
			}
		case agent.EventToolCall:
			fmt.Printf("  → %s(%s)\n", ev.ToolCall.Name, ev.ToolCall.Args)
		case agent.EventToolResult:
			if ev.ToolResult.IsError {
				fmt.Printf("  ✗ %s: %s\n", ev.ToolResult.CallID, ev.ToolResult.Content)
			} else {
				fmt.Printf("  ✓ %s: %s\n", ev.ToolResult.CallID, ev.ToolResult.Content)
			}
		case agent.EventResponse:
			fmt.Printf("\n%s\n", ev.Content)
		case agent.EventError:
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}
}

func handleStreamChat(client llm.LLM, messages *[]llm.Message) {
	var full strings.Builder
	for chunk, err := range client.ChatStream(context.Background(), *messages) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
			break
		}
		if chunk.Delta != "" {
			fmt.Print(chunk.Delta)
			full.WriteString(chunk.Delta)
		}
	}
	fmt.Println()

	if full.Len() > 0 {
		*messages = append(*messages, llm.AssistantMessage(full.String()))
	}
}

// handleToolChat demonstrates single-step tool calling:
// 1. Chat with tools → check if LLM wants to call tools
// 2. Execute tool calls → feed results back
// 3. Chat again for final answer
func handleToolChat(client llm.LLM, registry *tool.Registry, messages *[]llm.Message) {
	resp, err := client.Chat(
		context.Background(), *messages,
		llm.WithTools(registry.Schemas()...),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	if resp.StopReason != llm.StopReasonToolCall || len(resp.Message.ToolCalls) == 0 {
		fmt.Println(resp.Message.Content)
		*messages = append(*messages, resp.Message)
		return
	}

	*messages = append(*messages, resp.Message)

	fmt.Printf("[Calling %d tool(s)...]\n", len(resp.Message.ToolCalls))
	for _, tc := range resp.Message.ToolCalls {
		fmt.Printf("  → %s(%s)\n", tc.Name, tc.Args)
	}

	results := tool.ExecuteAll(context.Background(), registry, resp.Message.ToolCalls)
	for _, r := range results {
		if r.IsError {
			fmt.Printf("  ✗ %s: %s\n", r.CallID, r.Content)
		} else {
			fmt.Printf("  ✓ %s: %s\n", r.CallID, r.Content)
		}
		*messages = append(*messages, llm.ToolMessage(r.CallID, r.Content))
	}

	finalResp, err := client.Chat(
		context.Background(), *messages,
		llm.WithTools(registry.Schemas()...),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	fmt.Println(finalResp.Message.Content)
	*messages = append(*messages, finalResp.Message)
}

func createClient(provider, model, baseURL, apiKey string) llm.LLM {
	switch provider {
	case "anthropic", "claude":
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if model == "" {
			model = "claude-sonnet-4-6"
		}
		return anthropic.New(anthropic.Config{
			APIKey:  apiKey,
			BaseURL: baseURL,
			Model:   model,
		})
	default:
		if apiKey == "" {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
		if model == "" {
			model = "gpt-5.4"
		}
		return openai.New(openai.Config{
			APIKey:  apiKey,
			BaseURL: baseURL,
			Model:   model,
		})
	}
}
