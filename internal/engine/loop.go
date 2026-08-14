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
	composer       *ctxpkg.PromptComposer
	compactor      *ctxpkg.Compactor // 【新增】压缩器实例
}

// NewAgentEngine 【注意】：我们移除了 Engine 层级的 WorkDir，因为 WorkDir 应该跟随 Session 走！
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
		// (假装这里能获取到 WorkDir 初始化 Composer，生产环境中应在 Run 中动态构造)
		composer: ctxpkg.NewPromptComposer("."),
		//【初始化压缩器】：为了便于今天的极端测试，我们将水位线阈值设积极（例如 3000 字符）
		// 并保护最近的6条消息（大约两轮 Turn 的交互）
		compactor: ctxpkg.NewCompactor(3000, 6),
	}
}

// Run 【核心改造】：移除 userPrompt 参数，改为接收一个具体的 Session 实例
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区：%s\n", session.ID, session.WorkDir)

	// 根据当前 Session 的工作区，动态组装最新的 SystemPrompt
	e.composer = ctxpkg.NewPromptComposer(session.WorkDir)
	systemMsg := e.composer.Build()

	for {
		availableTools := e.registry.GetAvailableTools()

		// 1. 从 Session 提取出近期的 WorkingMemory（例如最近的 20 条，给压缩器留下充足判断空间）
		workingMemory := session.GetWorkingMemory(20)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		// 2. 【核心注入点】：在向 Provider 发起推理前，过一遍内存压缩器
		// 无论你带出了多少上下文，如果字符总数超标，早期日志将被掩码化，超大日志将被掐头去尾
		compactedContext := e.compactor.Compact(contextHistory)

		// 3. 后续的 Provider.Generate 全面使用被保护过的新鲜上下文（compactedContext）
		// Phase 1: 慢思考阶段（Thinking）- 剥夺工具，强制规划
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResp, err := e.provider.Generate(ctx, compactedContext, nil)
			if err != nil {
				return fmt.Errorf("thinking 阶段失败：%w", err)
			}

			if thinkResp.Content != "" {
				// 将思考过程持久化到 Session 中
				session.Append(*thinkResp)
				// 把它追加到当前这一轮的临时上下文中，供 Action 阶段使用
				compactedContext = append(compactedContext, *thinkResp)
			}

		}

		// ======================= Phase 2: Action ===============================
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return fmt.Errorf("action 阶段失败：%w", err)
		}

		//【驾驭精髓】：注意，写入 Session（硬盘/全量内存）的永远是全量的真实响应，不受 Compact 影响
		// Compact 只作用于本轮发给大模型的那个临时 Context
		session.Append(*actionResp)
		compactedContext = append(compactedContext, *actionResp)

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
