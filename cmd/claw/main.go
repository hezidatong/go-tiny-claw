package main

import (
	"context"
	"log"
	"os"

	ctxpkg "github.com/hezidatong/go-tiny-claw/internal/context"
	"github.com/hezidatong/go-tiny-claw/internal/engine"
	"github.com/hezidatong/go-tiny-claw/internal/feishu"
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

	// 假设一个bot绑定一个session
	sessionID := "test_command_intercept_001"
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(sessionID, workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: ""})

	bot := feishu.NewFeishuBot(eng, sess)
	//【核心注入】注册安全拦截 Middleware
	registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
		argsStr := string(call.Arguments)

		// 检查是否命中高危特征库
		if feishu.IsDangerousCommand(call.Name, argsStr) {
			taskID := call.ID // 使用大模型生成的唯一 ToolCallID 作为 TaskID

			// 挂起当前协程，发送消息给飞书，死死等待人类审批
			allowed, reason := feishu.GlobalApprovalMgr.WaitForApproval(taskID, call.Name, argsStr, bot.Reporter())
			if !allowed {
				return false, reason // 拒绝，将理由传回给大模型
			}
			return true, ""
		}

		// 没命中黑名单，直接YOLO 放行
		return true, ""
	})

	// 启动飞书终端

	log.Println("🚀 飞书 WebSocket 长连接模式启动...")
	if err := bot.StartWebSocket(context.Background()); err != nil {
		log.Fatalf("❌ WebSocket 连接失败: %v\n", err)
	}
}
