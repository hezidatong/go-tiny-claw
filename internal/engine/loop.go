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
	WorkDir        string
	EnableThinking bool
	composer       *ctxpkg.PromptComposer
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
		composer: ctxpkg.NewPromptComposer(workDir),
	}
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string, reporter Reporter) error {
	log.Printf("[Engine] 引擎启动，锁定工作区：%s\n", e.WorkDir)

	// 动态组装 System Prompt，彻底替换掉以前硬编码的面条提示词
	systemMsg := e.composer.Build()

	contextHistory := []schema.Message{
		systemMsg,  // 注入动态组装内核、AGENTS.md 与 Skills
		{Role: schema.RoleUser, Content: userPrompt},
	}

	turnCount := 0

	for {
		turnCount++
		availableTools := e.registry.GetAvailableTools()

		// Phase 1: 慢思考阶段（Thinking）- 剥夺工具，强制规划
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("thinking 生成失败：%w", err)
			}

			if thinkResp.Content != "" {
				contextHistory = append(contextHistory, *thinkResp)
			}

		}

		// Phase 2: Action
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("action 阶段生成失败：%w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		// 执行退出与并发控制
		if len(actionResp.ToolCalls) == 0 {
			break
		}

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

				obsMsg := schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: toolCall.ID,
				}
				observationMsgs[idx] = obsMsg

			}(i, toolCall)
		}

		wg.Wait()

		for _, obs := range observationMsgs {
			contextHistory = append(contextHistory, obs)
		}

	}

	return nil
}
