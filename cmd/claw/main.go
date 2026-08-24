package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

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
	// 1. 命令行参数解析
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
	workDirPtr := flag.String("dir", ".", "Agent 运行的工作区目录路径 (默认为当前目录)")
	sessionPtr := flag.String("session", "cli_default_session", "指定会话 ID，支持断点续传")
	flag.Parse()

	if *promptPtr == "" {
		fmt.Println("用法：go-tiny-claw -prompt \"你的任务描述\" [-dir /path/to/workdir] [-session session_id]")
		os.Exit(1)
	}

	// 解析工作区绝对路径
	workDir, err := filepath.Abs(*workDirPtr)
	if err != nil {
		log.Printf("解析工作区路径失败: %v", err)
	}

	fmt.Println("==================================================================")
	fmt.Printf("🚀 启动 go-tiny-claw CLI 引擎...\n")
	fmt.Printf("🔐 锁定工作区：%s\n", workDir)
	fmt.Println("==================================================================")

	// 2. 初始化核心基础服务
	var realProvider provider.LLMProvider
	modelName := "glm-4.5-air"
	realProvider = provider.NewZhipuOpenAIProvider(modelName)

	// 获取持久化 Session
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(*sessionPtr, workDir)

	//【全息监控装配】：用 CostTracker 将真实大脑包裹起来
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	// 3. 初始化工具与执行层
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 在 CLI 模式下。我们默认开启 YOLO 模式（全权信任本地执行），
	// 因此这里暂时不挂载 Feishu 审批 Middleware

	// 4. 初始化核心引擎（组装器内部会自动加载 Composer，Compactor，Recovery，Reminders）
	// 开启 EnableThinking = true
	eng := engine.NewAgentEngine(trackedProvider, registry, false, true)

	//【全息追踪装配】：初始化链路追踪 RootSpan
	ctx, rootSpan := observability.StartSpan(context.Background(), "CLI.TaskRun")
	rootSpan.AddAttribute("Prompt", *promptPtr)
	defer func() {
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, workDir, sess.ID)
	}()

	// 5. 初始化彩色终端输出器
	reporter := engine.NewTerminalReporter()

	fmt.Printf("\n🏹 收到任务：%s\n", *reporter)

	// 将用户的 Prompt 压入 Session 记忆
	sess.Append(schema.Message{Role: schema.RoleUser, Content: *promptPtr})

	// 发起冲锋：启动 MainLoop
	if err = eng.Run(ctx, sess, reporter); err != nil {
		log.Fatalf("\n🔥引擎运行崩溃: %v", err)
	}

	fmt.Println("\n==================================================================")
	fmt.Printf("⭐️ 任务圆满结束。总耗时：%v\n", time.Since(rootSpan.StartTime))
	fmt.Printf("💰 Session 累计消耗: $%.6f | Token: Input %d, Output %d\n",
		sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================================")
}
