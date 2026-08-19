package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
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
	PlanMode       bool                    // 暴露给外部的计划模式开关
	compactor      *ctxpkg.Compactor       //【新增】压缩器实例
	recovery       *ctxpkg.RecoveryManager //【新增】自愈管理器
	injector       *ReminderInjector       // 提醒注入器
}

// NewAgentEngine 【注意】：我们移除了 Engine 层级的 WorkDir，因为 WorkDir 应该跟随 Session 走！
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		EnableThinking: enableThinking,
		PlanMode:       planMode,
		//【初始化压缩器】：为了便于今天的极端测试，我们将水位线阈值设积极（例如 3000 字符）
		// 并保护最近的6条消息（大约两轮 Turn 的交互）
		compactor: ctxpkg.NewCompactor(20000, 6),
		recovery:  ctxpkg.NewRecoveryManager(),
		injector:  NewReminderInjector(), // 初始化注入器
	}
}

// Run 【核心改造】：移除 userPrompt 参数，改为接收一个具体的 Session 实例
func (e *AgentEngine) Run(ctx context.Context, session *ctxpkg.Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区：%s (PlanMode: %v)\n", session.ID, session.WorkDir, e.PlanMode)

	// 根据当前 Session 的工作区，动态组装最新的 SystemPrompt
	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode)
	systemMsg := composer.Build()

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

		var currentTurnThinkingContent string

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
				currentTurnThinkingContent = thinkResp.Content
				// 把它追加到当前这一轮的临时上下文中，供 Action 阶段使用
				compactedContext = append(compactedContext, *thinkResp)
			}

		}

		// ======================= Phase 2: Action ===============================
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return fmt.Errorf("action 阶段失败：%w", err)
		}

		finalAssistantMsg := schema.Message{
			Role:      schema.RoleAssistant,
			Content:   strings.TrimSpace(currentTurnThinkingContent + "\n" + actionResp.Content),
			ToolCalls: actionResp.ToolCalls,
		}

		//【驾驭精髓】：注意，写入 Session（硬盘/全量内存）的永远是全量的真实响应，不受 Compact 影响
		// Compact 只作用于本轮发给大模型的那个临时 Context
		session.Append(finalAssistantMsg)
		//compactedContext = append(compactedContext, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			// 如果没有工具调用，说明本次任务已完成，打破 ReAct 循环，挂起等待人类的下一条指令
			break
		}

		// Phase 3: 并发执行底层工具并注入自愈模板
		var wg sync.WaitGroup
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))

		// 收集本轮执行的最后一个工具，供 Reminder 探测器分析
		// （在真实的工业级架构中，如果并发调用了多个工具，可以逐个分析或仅分析报错的那个，此处简化为去第一个。）
		var lastToolCall schema.ToolCall
		var lastToolResult schema.ToolResult

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)

			go func(idx int, call schema.ToolCall) {
				defer wg.Done()

				if reporter != nil {
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}

				// 底层物理执行工具
				result := e.registry.Execute(ctx, call)

				// 【核心拦截与注入】
				finalOutput := result.Output
				if result.IsError {
					// 发生错误，交由 RecoveryManager 诊断并注入“锦囊妙计”
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
				}

				if reporter != nil {
					displayOutput := finalOutput
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "...(已截断)"
					}
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}

				// 将注入过 Recovery Hint 的最终结果写入上下文历史
				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    finalOutput,
					ToolCallID: call.ID,
				}

				// 捕获状态供外部探测器使用
				if idx == 0 {
					lastToolCall = call
					lastToolResult = result
				}

			}(i, toolCall)
		}

		wg.Wait()

		// 1.先将普通的工具执行结果存入 Session
		session.Append(observationMsgs...)

		// 2.【核心防线】：在准备进入下一轮之前，进行死循环探测
		reminderMsg := e.injector.CheckAndInject(lastToolCall, lastToolResult)
		if reminderMsg != nil {
			// 如果触发了干预规则，将这条严厉的提醒作为 User 消息，强制追加到 Session 的末尾！
			// 大模型在下一轮被唤醒时，第一眼就会看到这句话，从而打破局部执念。
			session.Append(*reminderMsg)
		}
	}

	return nil
}
