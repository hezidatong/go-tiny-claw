package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	ctxpkg "github.com/hezidatong/go-tiny-claw/internal/context"
	"github.com/hezidatong/go-tiny-claw/internal/provider"
	"github.com/hezidatong/go-tiny-claw/internal/schema"
	"github.com/hezidatong/go-tiny-claw/internal/tools"
)

type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	EnableThinking bool
}

// NewAgentEngine 【注意】：我们移除了 Engine 层级的 WorkDir，因为 WorkDir 应该跟随 Session 走！
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
	}
}

// Run 【核心改造】：移除 userPrompt 参数，改为接收一个具体的 Session 实例
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区：%s\n", session.ID, session.WorkDir)

	// 根据当前 Session 的工作区，动态组装最新的 SystemPrompt
	composer := ctxpkg.NewPromptComposer(session.WorkDir)
	systemMsg := composer.Build()

	for {
		availableTools := e.registry.GetAvailableTools()

		// 0. 【上下文组装】：SystemPrompt + 截取最近的 6 条消息作为 WorkingMemory
		// 在实际业务中，由于工具返回结果可能很长，短期工作记忆往往设为 6-10 条足以维系连贯对话
		workingMemory := session.GetWorkingMemory(6)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// Phase 1: 慢思考阶段（Thinking）- 剥夺工具，强制规划
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("thinking 阶段失败：%w", err)
			}

			if thinkResp.Content != "" {
				// 将思考过程持久化到 Session 中
				session.Append(*thinkResp)
				// 把它追加到当前这一轮的临时上下文中，供 Action 阶段使用
				contextHistory = append(contextHistory, *thinkResp)
			}

		}

		// Phase 2: Action
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("action 阶段失败：%w", err)
		}

		// 将大模型的行动响应持久化到 Session 中
		session.Append(*actionResp)
		contextHistory = append(contextHistory, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			// 如果没有工具调用，说明本次任务已完成，打破 ReAct 循环，挂起等待人类的下一条指令
			break
		}

		// Phase 3: 并发执行底层工具
		var wg sync.WaitGroup
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)

			go func(idx int, call schema.ToolCall) {
				defer wg.Done()

				if reporter != nil {
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				result := e.registry.Execute(ctx, toolCall)

				if reporter != nil {
					displayOutput := result.Output
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "...(已截断)"
					}
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}

				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: toolCall.ID,
				}

			}(i, toolCall)
		}

		wg.Wait()

		// 将所有的工具执行结果（Observation）持久化到 Session 中，开启下一轮的复盘与推理
		session.Append(observationMsgs...)
	}

	return nil
}
