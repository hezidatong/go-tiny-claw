package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/hezidatong/go-tiny-claw/internal/provider"
	"github.com/hezidatong/go-tiny-claw/internal/schema"
	"github.com/hezidatong/go-tiny-claw/internal/tools"
)

type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	WorkDir        string
	EnableThinking bool
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
	}
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	log.Printf("[Engine] 引擎启动，锁定工作区：%s\n", e.WorkDir)
	log.Printf("[Engine] 慢思考模式（Thinking Phase）：%v\n", e.EnableThinking)

	contextHistory := []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: "You are go-tiny-claw, an expert coding assistant. You have full access to tools in the workspace.",
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCount := 0

	for {
		turnCount++
		log.Printf("\n================= [Turn %d] 开始 ================\n", turnCount)

		availableTools := e.registry.GetAvailableTools()

		// Phase 1: 慢思考阶段（Thinking）- 剥夺工具，强制规划
		if e.EnableThinking {
			log.Println("[Engine][Phase 1]剥夺工具访问权，强制进入慢思考与规划阶段...")

			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("thinking 阶段生成失败：%w", err)
			}

			if thinkResp.Content != "" {
				fmt.Printf("🧠[内部思考 Trace]: %s\n", thinkResp.Content)
				contextHistory = append(contextHistory, *thinkResp)
			}

		}

		log.Println("[Engine][Phase 2]恢复工具挂载，等待模型采取行动...")

		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("action 阶段生成失败：%w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		if actionResp.Content != "" {
			fmt.Printf("[对外回复]：%s\n", actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			log.Println("[Engine] 模型未请求调用工具，任务宣告完成。")
			break
		}

		log.Printf("[Engine] 模型请求调用 %d 个工具...\n", len(actionResp.ToolCalls))

		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))

		var wg sync.WaitGroup

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)

			go func(idx int, call schema.ToolCall) {
				defer wg.Done()
				log.Printf("  -> [Go-%d] 🔨 触发并行执行：%s\n", idx, call.Name)

				result := e.registry.Execute(ctx, toolCall)
				if result.IsError {
					log.Printf(" -> [Go-%d] ❎ 工具执行报错：%s\n", idx, result.Output)
				} else {
					log.Printf(" -> [Go-%d] ✅ 工具执行成功 （返回 %d 字节）\n", idx, len(result.Output))
				}

				obsMsg := schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: toolCall.ID,
				}
				observationMsgs[idx] = obsMsg

			}(i, toolCall)
		}

		wg.Wait()
		log.Println("[Engine] 所有并发工具执行完毕，开始聚合观察结果 (Observation)...")

		for _, obs := range observationMsgs {
			contextHistory = append(contextHistory, obs)
		}

	}

	return nil
}
