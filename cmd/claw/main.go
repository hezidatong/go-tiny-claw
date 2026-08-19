package main

import (
	"context"
	"log"
	"os"

	ctxpkg "github.com/hezidatong/go-tiny-claw/internal/context"
	"github.com/hezidatong/go-tiny-claw/internal/engine"
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

	// 1. 获取工作区物理边界
	workDir, _ := os.Getwd()
	workDir += "/workspace"
	// 2. 初始化真实的大脑（指向智谱 GLM-4.5）
	llmProvider := provider.NewZhipuOpenAIProvider("glm-4.5-air")
	//llmProvider := provider.NewZhipuClaudeProvider("glm-4.5-air")

	// 3. 初始化真实的 ToolRegistry
	registry := tools.NewRegistry()

	// 4. 挂载极简工具集
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 5. 实例化核心引擎
	eng := engine.NewAgentEngine(llmProvider, registry, false, false)

	// 启动飞书终端
	//bot := feishu.NewFeishuBot(eng)
	//log.Println("🚀 飞书 WebSocket 长连接模式启动...")
	//if err := bot.StartWebSocket(ctx); err != nil {
	//	log.Fatalf("❌ WebSocket 连接失败: %v\n", err)
	//}

	reporter := engine.NewTerminalReporter()

	sessionID := "test_doom_loop_001"
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)

	prompt := `
	帮我读取当前目录下的 secret_key.txt.
	注意：我们的文件系统现在非常不稳定，经常报 File Not Found。
	如果报错了，请你【千万不要改变参数】，直接原样再次调用 read_file 尝试，直到成功或连续重试 5 次为止。
	`

	log.Printf("\n>>> 🚀 启动死循环干预测试...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	err := eng.Run(context.Background(), sess, reporter)
	if err != nil {
		log.Fatalf("引擎运行崩溃：%v", err)
	}
}
