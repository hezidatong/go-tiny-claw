package main

import (
	"context"
	"log"
	"os"

	ctxpkg "github.com/hezidatong/go-tiny-claw/internal/context"
	"github.com/hezidatong/go-tiny-claw/internal/engine"
	"github.com/hezidatong/go-tiny-claw/internal/observability"
	"github.com/hezidatong/go-tiny-claw/internal/provider"
	"github.com/hezidatong/go-tiny-claw/internal/schema"
	"github.com/hezidatong/go-tiny-claw/internal/tools"
)

type mockProvider struct {
	turn int
}

func (m *mockProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	if len(tools) == 0 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "【推理中】目标是检查文件。我不能直接盲猜，我需要先调用 bash 工具执行 ls 命令，看看当前目录下有什么，然后再做定夺。",
		}, nil
	}

	m.turn++
	if m.turn == 1 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "我要执行我刚才计划的步骤了。",
			ToolCalls: []schema.ToolCall{
				{ID: "call_123", Name: "bash", Arguments: []byte(`{"command": "ls -la"}`)},
			},
		}, nil
	}

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "根据工具返回的结果，我看到了 main.go，任务圆满完成！",
	}, nil
}

type mockRegistry struct{}

func (m *mockRegistry) GetAvailableTools() []schema.ToolDefinition {
	return []schema.ToolDefinition{
		{
			Name:        "get_weather",
			Description: "获取指定城市的当前天气情况",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"city": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []string{"city"},
			},
		},
	}
}

func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	log.Printf(" -> [Mock 工具执行] 获取 %s 的天气中...\n", call.Name)
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "API 返回：今天是晴天，气温 25 度",
		IsError:    false,
	}
}

func main() {
	// 通过命令行参数接收用户的 prompt
	//promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
	//flag.Parse()
	//
	//if *promptPtr == "" {
	//	fmt.Println("用法：go cun cmd/claw/main.go -prompt \"你的任务指令\"")
	//	os.Exit(1)
	//}
	// 确保设置了 ZHIPU_API_KEY
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
	}

	workDir, _ := os.Getwd()
	modelName := "glm-4.5-air"

	// 1. 初始化真实的底层大脑
	realProvider := provider.NewZhipuOpenAIProvider(modelName)

	sessionID := "test_observability_001"
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)

	// 2. 核心拼装：用 Tracker 将真实的大脑包裹起来
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	Registry := tools.NewRegistry()
	Registry.Register(tools.NewBashTool(workDir))

	// 3. 将被包裹的 Provider 注入给 Engine（Engine 毫不知情）
	eng := engine.NewAgentEngine(trackedProvider, Registry, false, false)
	reporter := engine.NewTerminalReporter()

	prompt := `请用 bash 帮我用 date 命令查一下现在的时间。`

	log.Println("\n>>> 🚀 启动带仪表盘的可观测性测试...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v\n", err)
	}

	log.Printf("\n===================== 财务报表====================\n")
	log.Printf("会话 ID: %s\n", sess.ID)
	log.Printf("总消耗 Input Tokens: %d\n", sess.TotalPromptTokens)
	log.Printf("总消耗 Output Tokens: %d\n", sess.TotalCompletionTokens)
	log.Printf("总计费用（CNY）: ¥%.6f\n", sess.TotalCostCNY)
	log.Printf("==================================================\n")
}
