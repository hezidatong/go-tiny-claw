package main

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

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
	// 确保设置了 ZHIPU_API_KEY
	if os.Getenv("ZHIPU_API_KEY") == "" {
		log.Fatal("请先导出 ZHIPU_API_KEY 环境变量")
	}

	// 1. 获取工作区物理边界
	//workDir, _ := os.Getwd()
	//workDir += "/workspace"
	// 2. 初始化真实的大脑（指向智谱 GLM-4.5）
	llmProvider := provider.NewZhipuOpenAIProvider("glm-4.5-air")
	//llmProvider := provider.NewZhipuClaudeProvider("glm-4.5-air")

	// 3. 初始化真实的 ToolRegistry
	registry := tools.NewRegistry()

	// 4. 挂载极简工具集
	registry.Register(tools.NewReadFileTool("/tmp/project_front"))
	//registry.Register(tools.NewWriteFileTool(workDir))
	//registry.Register(tools.NewBashTool(workDir))
	//registry.Register(tools.NewEditFileTool(workDir))

	// 5. 实例化核心引擎
	eng := engine.NewAgentEngine(llmProvider, registry, false)

	// 启动飞书终端
	//bot := feishu.NewFeishuBot(eng)
	//log.Println("🚀 飞书 WebSocket 长连接模式启动...")
	//if err := bot.StartWebSocket(ctx); err != nil {
	//	log.Fatalf("❌ WebSocket 连接失败: %v\n", err)
	//}

	reporter := engine.NewTerminalReporter()

	var wg sync.WaitGroup

	// ================== 模拟并发场景 1：飞书前端群 =========================
	wg.Add(1)
	go func() {
		defer wg.Done()
		sessionA := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_front_001", "tmp/project_front")

		// 回合 1：获取机密
		log.Println("\n>>> 👦🏻 [Session A / Turn 1]: 帮我看看 README.md 里记录了什么密钥？")
		sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "帮我看看 README.md 里记录了什么密钥？"})
		_ = eng.Run(context.Background(), sessionA, reporter)

		// 故意制造大量“废话”对话，刷掉记忆（假设 Working Memory Limit=6）
		for i := 0; i < 6; i++ {
			sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "这只是一句闲聊占位符。"})
			sessionA.Append(schema.Message{Role: schema.RoleAssistant, Content: "好的，收到闲聊。"})
		}

		// 回合 2：验证记忆截断（此时第一轮的密钥已经被挤出 WorkingMemory了！）
		log.Println("\n>>> 👦🏻 [Session A / Turn 2]: 请直接告诉我，刚才第一轮你查到的那个密钥是什么？")
		sessionA.Append(schema.Message{Role: schema.RoleUser, Content: "请直接告诉我，刚才第一轮你查到的那个密钥是什么？不准调用工具！"})
		_ = eng.Run(context.Background(), sessionA, reporter)
	}()

	// ================== 模拟并发场景 2：飞书后端群 =========================
	wg.Add(1)
	go func() {
		defer wg.Done()
		// 稍微错开一点时间发起请求
		time.Sleep(time.Second)

		sessionB := ctxpkg.GlobalSessionMgr.GetOrCreate("chat_back_002", "tmp/project_back")
		log.Println(">>> 👦🏻 [Session B]: 别人查到了一个密钥，你这里能看到吗？")
		sessionB.Append(schema.Message{Role: schema.RoleUser, Content: "别人查到了一个密钥，你这里能看到吗？不准调用工具！"})
		_ = eng.Run(context.Background(), sessionB, reporter)
	}()

	wg.Wait()
}
