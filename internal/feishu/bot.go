package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	ctxpkg "github.com/hezidatong/go-tiny-claw/internal/context"
	"github.com/hezidatong/go-tiny-claw/internal/engine"
	"github.com/hezidatong/go-tiny-claw/internal/schema"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"
)

// ==================================================
// 1. Context 传递机制：解决并发 Reporter 的提取
// ==================================================

// reporterKey 定义 Context 中存放 Reporter 的专属键
type reporterKey struct{}

// ContextWithReporter 将专属的 Reporter 封入上下文
func ContextWithReporter(ctx context.Context, r engine.Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// ReporterFromContext 供底层的 Middleware 提取专属的 Reporter 发送审批卡片
func ReporterFromContext(ctx context.Context) engine.Reporter {
	if r, ok := ctx.Value(reporterKey{}).(engine.Reporter); ok {
		return r
	}
	return nil
}

// ==================================================
// 2. 飞书 Bot 核心调度器
// ==================================================

// AgentEngineFactory 允许每次收到消息时，根据 Session 动态创建引擎
type AgentEngineFactory func(session *ctxpkg.Session) *engine.AgentEngine

type FeishuBot struct {
	client    *lark.Client
	appId     string
	appSecret string
	workDir   string             // 保存从入口传来的工作区路径
	factory   AgentEngineFactory // 替换掉原来的单一 engine 引用
}

func NewFeishuBotWithFactory(workDir string, factory AgentEngineFactory) *FeishuBot {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	if appID == "" || appSecret == "" {
		log.Fatal("请设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET")
	}

	client := lark.NewClient(appID, appSecret)

	return &FeishuBot{
		client:    client,
		appId:     appID,
		appSecret: appSecret,
		workDir:   workDir, // 接收外部传入的路径
		factory:   factory,
	}
}

// StartWebSocket 启动 WebSocket 长连接方式接收飞书事件（推荐方式）
// 优势：无需公网 IP、无需配置回调 URL 、自动重连、部署简单
func (b *FeishuBot) StartWebSocket(ctx context.Context) error {
	log.Println("🔌 正在启动 WebSocket 长连接方式...")
	// 创建事件处理器（长连接模式下 verifyToken 和 encryptKey 可以为空）
	eventDispatcher := b.createEventDispatcher("", "")

	wsClient := ws.NewClient(
		b.appId,
		b.appSecret,
		ws.WithEventHandler(eventDispatcher),
		ws.WithLogLevel(larkcore.LogLevelInfo),
		ws.WithAutoReconnect(true), // 自动重连
	)

	log.Println("✅ WebSocket 客户端已创建，正在连接飞书服务器...")
	// 启动长连接（阻塞式调用，会一直运行直到连接断开或 context 取消）
	return wsClient.Start(ctx)
}

// GetEventDispatcher 用于注册到 HTTP 服务器，处理来自飞书的 POST 事件（传统方式）
// 注意：HTTP 回调方式需要公网 IP 和配置回调 URL，推荐使用 StartWebSocket 方式
func (b *FeishuBot) GetEventDispatcher() *dispatcher.EventDispatcher {
	encryptKey := os.Getenv("FEISHU_ENCRYPT_KEY")
	verifyToken := os.Getenv("FEISHU_VERIFY_TOKEN")

	return b.createEventDispatcher(verifyToken, encryptKey)
}

func (b *FeishuBot) createEventDispatcher(verifyToken, encryptKey string) *dispatcher.EventDispatcher {
	// 使用官方 SDK 构建调度器，监听 "接收消息" 事件
	handler := dispatcher.NewEventDispatcher(verifyToken, encryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			// 由于飞书消息体是 JSON，我们需要粗略地提取其中的文本内容。
			// 这里简单处理：去掉开头结尾的特殊转义字符和引用的机器人名字。
			contentStr := *event.Event.Message.Content
			contentStr = strings.TrimPrefix(contentStr, `{"text":"`)
			contentStr = strings.TrimSuffix(contentStr, `"}`)

			chatId := *event.Event.Message.ChatId
			log.Printf("[Feishu] 收到会话 %s 消息：%s\n", chatId, contentStr)

			//【新增】: 拦截人工审批的特殊口令
			if strings.HasPrefix(contentStr, "approve ") {
				taskID := strings.TrimPrefix(contentStr, "approve ")
				taskID = strings.TrimSpace(taskID)
				// 唤醒挂起的引擎协程
				GlobalApprovalMgr.ResolveApproval(taskID, true, "人类管理员已批准操作")
				log.Printf("[Feishu] 会话 %s: ✅ 已为您批准任务 %s", chatId, taskID)
				return nil
			}
			if strings.HasPrefix(contentStr, "reject ") {
				taskID := strings.TrimPrefix(contentStr, "reject ")
				taskID = strings.TrimSpace(taskID)
				// 唤醒挂起的引擎协程，并反馈拒绝理由
				GlobalApprovalMgr.ResolveApproval(taskID, false, "人类管理员认为该操作存在极高风险，已无情拒绝")
				log.Printf("[Feishu] 会话 %s: 🈲 已拒绝任务 %s", chatId, taskID)
				return nil
			}

			// 如果不是审批命令，则是正常对话，启动一个新的 Agent 任务去处理
			go b.handleAgentRun(chatId, contentStr)

			return nil
		}).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			// 消息已读事件，静默忽略
			return nil
		})

	return handler
}

func (b *FeishuBot) handleAgentRun(chatId string, prompt string) {
	// 为当前并发请求实例化一个专属的 Reporter
	reporter := &FeishuReporter{
		client: b.client,
		chatId: chatId,
	}

	// 1. 获取物理隔离的 Session
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(chatId, b.workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt}) // 将 prompt 加入会话中

	// 2. 通过工厂模式，为当前会话生成一个挂载了专属 CostTracker 的新引擎
	eng := b.factory(sess)

	// 3.【驾驭核心】：将专属的 reporter 塞入 Context 并传给引擎
	runCtx := ContextWithReporter(context.Background(), reporter)

	err := eng.Run(runCtx, sess, reporter)
	if err != nil {
		reporter.sendMsg(fmt.Sprintf("❌ Agent 运行崩溃：%v", err))
	}
}

// ==================================================
// 3. 飞书 Reporter 实现
// ==================================================

type FeishuReporter struct {
	client *lark.Client
	chatId string
}

func (r *FeishuReporter) sendMsg(text string) {
	textContent := map[string]string{
		"text": text,
	}

	contentBytes, _ := json.Marshal(textContent)
	contentStr := string(contentBytes)

	msgReq := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(r.chatId).
			MsgType(larkim.MsgTypeText).
			Content(contentStr).Build()).
		Build()

	_, _ = r.client.Im.Message.Create(context.Background(), msgReq)
}

func (r *FeishuReporter) OnThinking(ctx context.Context) {
	r.sendMsg("模型正在慢思考（Thinking）...")
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.sendMsg(fmt.Sprintf("🔨 **正在执行工具**：`%s`\n参数：`%s`", toolName, args))
}

func (r *FeishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(fmt.Sprintf("⚠️ **执行报错** (%s): \n%s", toolName, result))
	} else {
		r.sendMsg(fmt.Sprintf("✅ **执行成功** (%s)", toolName))
	}
}

func (r *FeishuReporter) OnMessage(ctx context.Context, content string) {
	r.sendMsg(content)
}

var _ engine.Reporter = (*FeishuReporter)(nil)
