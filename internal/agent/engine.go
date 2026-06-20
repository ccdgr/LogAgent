package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/xy/LogAgent/internal/db"
	"github.com/xy/LogAgent/internal/model"
	"github.com/xy/LogAgent/internal/util"
)

const (
	MaxIterations   = 10 // 硬上限：单次诊断最多 10 轮工具调用
	ConvergeTarget  = 5  // 优化目标：中等复杂度场景应在 5 轮内收敛
	scanMaxMatches  = 20
)

// CallStats 单次诊断的调用效率统计
type CallStats struct {
	Total     int `json:"total"`     // 总工具调用次数
	Valid     int `json:"valid"`     // 有效调用（返回了实际数据）
	Redundant int `json:"redundant"` // 逻辑冗余调用（返回了数据，但数据已经在之前的 Observation 中出现过）
	Invalid   int `json:"invalid"`   // 无效调用（空参数、未命中、重复、错误）
}

// InvalidRatio 返回无效调用占比（0~1），Redundant 计入分母但不计入分子（不算严格无效）
func (s CallStats) InvalidRatio() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Invalid) / float64(s.Total)
}

// WasteRatio 返回浪费调用占比（无效 + 冗余）
func (s CallStats) WasteRatio() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Invalid+s.Redundant) / float64(s.Total)
}

// IsHealthy 判断调用质量是否达标（无效调用 ≤20%）
func (s CallStats) IsHealthy() bool {
	return s.InvalidRatio() <= 0.2
}

type Agent struct {
	TaskID       string
	mu           sync.Mutex
	seenSearches map[string]bool
	seenTraceIDs map[string]bool // 已出现过的 trace_id，用于检测逻辑冗余扫描
	stats        CallStats
}

func NewAgent(taskID string) *Agent {
	return &Agent{
		TaskID:       taskID,
		seenSearches: make(map[string]bool),
		seenTraceIDs: make(map[string]bool),
	}
}

func (a *Agent) LogStep(stepType, content string) {
	trace := model.TraceLog{
		TaskID:  a.TaskID,
		Type:    stepType,
		Content: content,
	}
	db.DB.Create(&trace)

	msg, _ := json.Marshal(map[string]string{
		"type":    stepType,
		"content": content,
	})
	db.RDB.Publish(context.Background(), fmt.Sprintf("task:trace:%s", a.TaskID), msg)
}

// buildSystemPrompt 构造系统提示词
func buildSystemPrompt() string {
	return `你是一个顶级的后端故障诊断专家。你必须通过以下工具来分析 logs/ 目录下的日志并解决问题：
1. list_logs[]: 列出所有日志文件。
2. scan_log[filename, keyword]: 搜索关键词，返回匹配的【行号】和内容（最多 20 条，超出会截断并提示）。这是定位错误的初步手段。
3. read_context[filename, line_num, range]: 读取指定行号前后的上下文。当你发现 Panic 或 Error 的行号后，必须使用此工具获取堆栈详情。建议 range 设为 10~20 以覆盖完整调用链路和前置关联日志。

【铁律 - 严禁演独角戏】
- 你只是一个协助决策的 AI，不具备直接读取本地文件的权限。
- 当你输出完 "Action Input:" 后，你必须立刻停止一切输出！
- 严禁自行编造、续写 "Observation:" 及其后面的任何内容。真正的 Observation 会由系统在下一轮对话中提供。

【Action Input 参数格式 — 严格按此格式，不要添加任何多余字符】

Action 和 Action Input 必须分两行写。Action Input 的内容就是纯逗号分隔的参数值，不要加方括号、双引号、等号前缀。

✅ 正确写法：
  Action: scan_log
  Action Input: app.error.log, 10:33:35

  Action: scan_log
  Action Input: app.error.log, trace-panic-123456789abc

  Action: read_context
  Action Input: app.error.log, 172, 10

  Action: list_logs
  Action Input: 无参数

❌ 错误写法（会导致工具无法识别）：
  Action: scan_log[app.error.log, 10:33:35]       ← 不要加方括号
  Action Input: filename=app.error.log, keyword=xx  ← 不要加 filename= 等前缀
  Action Input: "app.error.log", "10:33:35"         ← 不要加引号
- scan_log 使用纯子串匹配（substring），按行逐行扫描，不区分大小写。
- 不支持 AND / OR / NOT 等布尔逻辑。如果你输入 "uid=1003 AND error"，系统会把这整个字符串当作一个关键词去匹配，结果必然为空。
- 正确做法：只使用一个最精炼的、唯一性高的关键词片段，例如：
  - 搜用户：直接搜 "1003" 或 "uid":1003（JSON 日志中 uid 字段的精确写法）
  - 搜时间：直接搜 "10:33:35" 或 "2026-06-17T10:33"（时间戳的前缀片段）
  - 搜 trace：直接搜完整的 trace_id 如 "trace-panic-123456789abc"
  - 搜错误类型：搜 "panic"、"connection pool exhausted"、"too many connections"
- 如果一次搜索返回了截断提示，说明匹配太多，必须用更精确的关键词缩小范围后再搜。
- 每次搜索前先想：我的关键词在日志原文中会以什么样子出现？选最短的能唯一定位的片段。

【高效检索策略 — 节省轮次】
- 已知 uid 和时间点，第一轮就搜时间片段（如 "10:33:35" 或 "10:33"），JSON 日志中每行都有时间戳，一次命中。
- 如果上一步 scan_log 的结果中已经看到某个 trace_id 的多条日志，不要再重复 scan 同一个 trace_id——这会被系统标记为「逻辑冗余调用」。直接用 read_context 扩展关键行附近的上下文（range 建议 10~20）。
- 如果 scan_log 返回「未发现匹配项」，立即检查：你的关键词是否用了 AND/OR？是否加了多余的空格或符号？立即改用更短、更原始的子串重试。

【轮次预算管理 — 严格执行】
- 你最多有 10 轮工具调用机会（硬上限），但工程优化目标是 5 轮内收敛。
- 前 3 轮：自由搜索，定位目标 trace_id 和关键错误。
- 第 4~5 轮：盘点已收集数据。如果已拿到 trace_id + 调用链 + 错误行号，立即输出 Final Answer。
- 第 6~9 轮：仅在数据严重不足时使用。每一轮都需要自问「这一轮之后我能不能写结论？」
- 第 10 轮：硬上限，系统强制拒绝工具调用，必须输出 Final Answer。
- 每次空参数、重复搜索、未命中都会计入无效调用统计。无效调用超过 20% 即视为低效诊断。

【意图识别约束】
- 如果用户的输入与系统故障、日志分析完全无关，你严禁调用任何日志工具，必须直接在 "Final Answer:" 中输出拒绝信息。

【收敛判定 — 满足任一条件立即输出 Final Answer】
- 已拿到目标 trace_id 的完整调用链路
- 已定位到 panic/error 的具体代码行号，且获得了前后上下文
- 已发现前置关联故障（连接池耗尽、慢查询），且目标请求日志已覆盖
- 当前已是第 5 轮或更后，数据基本完备就不应再搜

你必须严格遵守以下格式输出。不要使用 markdown 粗体、代码块或任何格式化标记：

Thought: 你的思考过程。
Action: 工具名称（list_logs, scan_log, 或 read_context）。
Action Input: 参数。
Observation: 系统返回的结果（你严禁输出此标签）。

一旦你找到了问题的根源，请严格按照以下模板输出最终诊断报告。
⚠️ 报告中每个章节都必须有实质内容，严禁只写标题。
⚠️ 调用链路必须列出具体的行号和文件名。
⚠️ 原因分析必须引用日志中的具体字段（如 latency、error、trace_id）。

Final Answer:
## 一、请求完整调用链路（trace_id，uid）
按时间顺序列出每一步。缺失环节标注 [数据缺失]。
示例：「1. LoggingMiddleware 接收请求 (行167) → 2. RateLimitMiddleware 校验放行 (行168) → ...」

## 二、500 报错分层原因
- **直接原因**：（哪个文件哪一行，什么操作触发了什么异常，最终返回 500）
- **底层根因**：（数据库/缓存/连接池等基础设施层面的根本问题）

## 三、故障发生先后顺序与传导关系
按时间线列出：最先发生的基础设施故障 → 中间衍生的性能退化 → 最终触发的业务异常，说明三者之间的因果关系。`
}

// Start 启动动态 ReAct 循环
func (a *Agent) Start(desc string) {
	messages := []util.DeepSeekMessage{
		{Role: "system", Content: buildSystemPrompt()},
		{Role: "user", Content: "故障现象：" + desc},
	}

	var finalAnswer string

	for i := 0; i < MaxIterations; i++ {
		// 1. 调用 AI 获取下一步行动
		resp, err := util.CallDeepSeek(messages)
		if err != nil {
			a.LogStep("final", "AI 接口调用异常: "+err.Error())
			a.updateTaskStatus("failed", "AI 接口调用异常: "+err.Error())
			return
		}

		// 2. 解析 AI 输出
		thought := extractValue(resp, "Thought:")
		action := sanitizeAction(extractValue(resp, "Action:"))
		actionInput := sanitizeInput(extractValue(resp, "Action Input:"))
		finalAnswer = extractValue(resp, "Final Answer:")

		if thought != "" {
			a.LogStep("thought", thought)
		}

		// 3. 检查是否得出最终结论
		if finalAnswer != "" {
			a.LogStep("final", finalAnswer)
			a.updateTaskStatus("success", finalAnswer)
			return
		}

		// 4. 执行 Action
		observation := ""
		if action != "" {
			// 硬上限：最后一轮拒绝工具调用
			if i == MaxIterations-1 {
				a.LogStep("thought", fmt.Sprintf("第 %d 轮（硬上限）尝试调用工具被拒绝", i+1))
				messages = append(messages, util.DeepSeekMessage{Role: "assistant", Content: resp})
				messages = append(messages, util.DeepSeekMessage{Role: "user", Content: fmt.Sprintf("⚠️ 已到达硬上限（第 %d/%d 轮），工具调用被系统拒绝。你必须基于以上所有 Observation 直接输出 Final Answer。", i+1, MaxIterations)})
				continue
			}

			a.LogStep("action", fmt.Sprintf("[第%d轮] 执行工具 %s, 参数: %s", i+1, action, actionInput))
			observation = a.executeTool(action, actionInput)
			a.stats.Total++

			// 分类统计：有效 vs 冗余 vs 无效调用
			if isObservationValid(observation) {
				a.stats.Valid++
				// 检测逻辑冗余：scan_log 返回了已见过的 trace_id
				if strings.TrimSpace(action) == "scan_log" && a.isTraceIDRedundant(observation) {
					a.stats.Redundant++
					observation += fmt.Sprintf("\n\n💡 效率提示：此搜索结果中包含已见过的 trace_id，属于逻辑冗余调用（已计入统计）。下次遇到已知 trace_id 请直接用 read_context 扩展上下文。")
				}
			} else {
				a.stats.Invalid++
			}

			// Observation 中追加调用统计信息
			obsWithStats := fmt.Sprintf("Observation: %s\n[调用统计] 总调用: %d, 有效: %d, 冗余: %d, 无效: %d (浪费率 %.0f%%, 阈值 20%%)",
				observation, a.stats.Total, a.stats.Valid, a.stats.Redundant, a.stats.Invalid, a.stats.WasteRatio()*100)

			a.LogStep("observation", obsWithStats)
		} else {
			messages = append(messages, util.DeepSeekMessage{Role: "assistant", Content: resp})
			messages = append(messages, util.DeepSeekMessage{Role: "user", Content: "请继续分析：你需要输出 Action 调用工具，或者输出 Final Answer 给出结论。"})
			continue
		}

		// 5. 将结果喂回 AI
		messages = append(messages, util.DeepSeekMessage{Role: "assistant", Content: resp})

		obsMsg := "Observation: " + observation
		// 倒数第二轮起：逐轮升级警告
		if i == ConvergeTarget-2 {
			obsMsg += fmt.Sprintf("\n\n⏰ 优化目标提醒：你已使用 %d/%d 轮，应在 %d 轮内收敛。如果当前数据已足够，请在下一轮直接输出 Final Answer。", i+1, MaxIterations, ConvergeTarget)
		} else if i >= ConvergeTarget-1 && i < MaxIterations-2 {
			obsMsg += fmt.Sprintf("\n\n⚠️ 已超出优化目标（%d 轮），当前第 %d/%d 轮。请尽快盘点数据，准备输出 Final Answer。", ConvergeTarget, i+1, MaxIterations)
		} else if i == MaxIterations-2 {
			obsMsg += fmt.Sprintf("\n\n🔴 最后警告：下一轮是硬上限（第 %d/%d 轮），届时工具将被强制拒绝。必须在下一轮输出 Final Answer！", i+1, MaxIterations)
		}
		// 附加实时统计
		obsMsg += fmt.Sprintf("\n[调用统计] 总调用: %d, 有效: %d, 冗余: %d, 无效: %d (浪费率 %.0f%%, 阈值 20%%)",
			a.stats.Total, a.stats.Valid, a.stats.Redundant, a.stats.Invalid, a.stats.WasteRatio()*100)
		messages = append(messages, util.DeepSeekMessage{Role: "user", Content: obsMsg})
	}

	// === 轮次耗尽兜底机制 ===
	a.LogStep("thought", fmt.Sprintf("推理轮次已达硬上限（%d 轮），正在压缩上下文并生成精简结论...", MaxIterations))
	fallbackResult := a.generateFallbackSummary(messages, desc)
	a.LogStep("final", fallbackResult)
	a.updateTaskStatus("success", fallbackResult)
}

// generateFallbackSummary 轮次耗尽时，压缩上下文后生成精简结论。
func (a *Agent) generateFallbackSummary(messages []util.DeepSeekMessage, originalDesc string) string {
	facts := a.extractKeyFacts(messages)

	qualityVerdict := "✅ 达标"
	if !a.stats.IsHealthy() {
		qualityVerdict = "❌ 未达标（无效调用超过 20%）"
	}

	fallbackPrompt := fmt.Sprintf(`你是一个后端故障诊断专家。以下是针对故障「%s」进行自动排查时收集到的所有关键数据。

=== 已收集的日志数据 ===
%s
=== 数据结束 ===

现在请基于以上数据，编写最终诊断报告。要求：
1. 只基于上面的数据，不要编造未出现的信息
2. 如果某项数据缺失，在对应位置标注 [数据缺失]
3. 直接输出以下格式，不要输出任何其他内容

Final Answer:
## 一、请求完整调用链路（trace_id，uid）
按时间顺序列出每一步（中间件→限流→Service→缓存→DAO→异常→恢复→结束）。

## 二、500 报错分层原因
- **直接原因**：（具体文件行号 + 操作 + 异常类型）
- **底层根因**：（基础设施层面的根本问题）

## 三、故障发生先后顺序与传导关系
按时间线列出因果关系。

## 四、数据完整性说明
- 已获取数据：列出本次实际获取到的关键信息
- 缺失数据：列出未能获取的信息，建议人工补充排查的方向

## 五、调用效率统计
- 总调用次数：%d
- 有效调用：%d
- 逻辑冗余调用：%d（数据已在前序 Observation 中出现）
- 无效调用：%d
- 无效调用占比：%.0f%% | 浪费率（无效+冗余）：%.0f%%
- 质量判定：%s
- 硬上限：%d 次 | 优化目标：%d 次内收敛`, originalDesc, facts,
		a.stats.Total, a.stats.Valid, a.stats.Redundant, a.stats.Invalid,
		a.stats.InvalidRatio()*100, a.stats.WasteRatio()*100, qualityVerdict,
		MaxIterations, ConvergeTarget)

	resp, err := util.CallDeepSeek([]util.DeepSeekMessage{
		{Role: "system", Content: "你是一个后端故障诊断专家，请基于给定的日志数据编写结构化诊断报告。"},
		{Role: "user", Content: fallbackPrompt},
	})
	if err != nil {
		return fmt.Sprintf("轮次耗尽且兜底调用失败: %s。请人工介入排查。\n调用统计: 总 %d / 有效 %d / 无效 %d (%.0f%%)",
			err.Error(), a.stats.Total, a.stats.Valid, a.stats.Invalid, a.stats.InvalidRatio()*100)
	}

	finalAnswer := extractValue(resp, "Final Answer:")
	if finalAnswer != "" {
		return finalAnswer
	}

	return "【轮次耗尽自动总结】\n" + resp
}

// isObservationValid 判断一次工具调用的观察结果是否为有效调用。
// 有效 = 返回了实际数据；无效 = 错误、空结果、重复、参数缺失。
func isObservationValid(obs string) bool {
	invalidPrefixes := []string{
		"错误:",
		"未发现匹配项",
		"提示：关键词",
		"未知工具:",
		"扫描失败:",
		"读取失败:",
	}
	for _, p := range invalidPrefixes {
		if strings.HasPrefix(obs, p) {
			return false
		}
	}
	if strings.TrimSpace(obs) == "" {
		return false
	}
	return true
}

// isTraceIDRedundant 检查 scan_log 返回结果中的 trace_id 是否在之前的 Observation 中已经出现过。
// 如果所有 trace_id 都已被记录过，则判定为逻辑冗余调用。
func (a *Agent) isTraceIDRedundant(obs string) bool {
	traceIDs := extractTraceIDs(obs)
	if len(traceIDs) == 0 {
		return false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	allSeen := true
	for _, id := range traceIDs {
		if !a.seenTraceIDs[id] {
			a.seenTraceIDs[id] = true
			allSeen = false
		}
	}
	return allSeen // 只有全部 trace_id 都已见过才算冗余
}

// extractTraceIDs 从 Observation 文本中提取所有 trace_id
func extractTraceIDs(text string) []string {
	re := regexp.MustCompile(`"trace_id":"([^"]+)"`)
	matches := re.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var ids []string
	for _, m := range matches {
		if len(m) > 1 && !seen[m[1]] {
			ids = append(ids, m[1])
			seen[m[1]] = true
		}
	}
	return ids
}

// extractKeyFacts 从消息历史中提取 Observation，压缩为紧凑事实汇总。
func (a *Agent) extractKeyFacts(messages []util.DeepSeekMessage) string {
	const maxPerObs = 2000
	const maxTotal = 8000

	var parts []string
	totalLen := 0

	for _, m := range messages {
		if m.Role != "user" || !strings.HasPrefix(m.Content, "Observation:") {
			continue
		}
		content := strings.TrimPrefix(m.Content, "Observation: ")
		content = strings.TrimSpace(content)

		if len(content) > maxPerObs {
			content = content[:1500] + "\n... [中间省略] ...\n" + content[len(content)-500:]
		}

		remaining := maxTotal - totalLen
		if remaining <= 0 {
			parts = append(parts, "... (更多 Observation 已省略)")
			break
		}
		if len(content) > remaining {
			content = content[:remaining] + "\n... (截断)"
		}

		parts = append(parts, content)
		totalLen += len(content)
	}

	if len(parts) == 0 {
		return "（未收集到任何 Observation 数据）"
	}

	return strings.Join(parts, "\n---\n")
}

// updateTaskStatus 同步更新任务状态和结果
func (a *Agent) updateTaskStatus(status, result string) {
	db.DB.Model(&model.Task{}).Where("id = ?", a.TaskID).Updates(map[string]interface{}{
		"status": status,
		"result": result,
	})
}

// executeTool 工具分发器（带去重保护）
func (a *Agent) executeTool(name, input string) string {
	switch strings.TrimSpace(name) {
	case "list_logs":
		files, err := os.ReadDir("logs")
		if err != nil {
			return "错误: 无法读取目录: " + err.Error()
		}
		var names []string
		for _, f := range files {
			names = append(names, f.Name())
		}
		return "发现文件: " + strings.Join(names, ", ")

	case "scan_log":
		parts := strings.Split(input, ",")
		if len(parts) < 2 {
			return "错误: scan_log 需要 [filename, keyword]"
		}
		filename := strings.TrimSpace(parts[0])
		keyword := strings.TrimSpace(parts[1])

		searchKey := filename + "|" + strings.ToLower(keyword)
		a.mu.Lock()
		if a.seenSearches[searchKey] {
			a.mu.Unlock()
			return fmt.Sprintf("提示：关键词 '%s' 在文件 %s 中已经搜索过，请直接使用之前的搜索结果，或换一个关键词继续排查。", keyword, filename)
		}
		a.seenSearches[searchKey] = true
		a.mu.Unlock()

		results, truncated, err := util.ScanLogFile("logs/"+filename, keyword, scanMaxMatches)
		if err != nil {
			return "扫描失败: " + err.Error()
		}
		if len(results) == 0 {
			return "未发现匹配项。提示：scan_log 使用纯子串匹配，不支持 AND/OR 布尔逻辑。请尝试更短、更原始的关键词片段。"
		}
		var out []string
		for _, r := range results {
			out = append(out, fmt.Sprintf("行号 [%d]: %s", r.LineNum, r.Content))
		}
		result := "搜索结果 (" + fmt.Sprintf("%d", len(results)) + " 条):\n" + strings.Join(out, "\n")
		if truncated {
			result += fmt.Sprintf("\n\n⚠️ 结果已截断！仅展示了前 %d 条，实际匹配更多。请使用更精确的关键词（如完整的 trace_id 或毫秒级时间戳）缩小范围后重新搜索。", scanMaxMatches)
		}
		return result

	case "read_context":
		parts := strings.Split(input, ",")
		if len(parts) < 3 {
			return "错误: read_context 需要 [filename, line_num, range]"
		}
		filename := strings.TrimSpace(parts[0])
		lineNum, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		offset, _ := strconv.Atoi(strings.TrimSpace(parts[2]))

		content, err := util.GetLogContext("logs/"+filename, lineNum, offset, offset)
		if err != nil {
			return "读取失败: " + err.Error()
		}
		return content

	default:
		return "未知工具: " + name
	}
}

// sanitizeAction 清洗模型可能附加的 markdown 格式（**bold**、多余空格）
func sanitizeAction(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "* ") // 去掉 markdown 粗体 **
	return s
}

// sanitizeInput 清洗 Action Input 中模型可能添加的方括号、引号、前缀
func sanitizeInput(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "[]\"'* ") // 去掉可能被误加的方括号和引号
	return s
}

// extractValue 简单的正则解析器。
// Final Answer 使用 (?s) 跨行匹配（报告内容多行），其余标签使用 (?m) 单行匹配。
func extractValue(text, key string) string {
	if key == "Final Answer:" {
		re := regexp.MustCompile(fmt.Sprintf(`(?s)%s\s*(.*)`, regexp.QuoteMeta(key)))
		match := re.FindStringSubmatch(text)
		if len(match) > 1 {
			return strings.TrimSpace(match[1])
		}
		return ""
	}
	re := regexp.MustCompile(fmt.Sprintf(`(?m)%s\s*(.*)`, regexp.QuoteMeta(key)))
	match := re.FindStringSubmatch(text)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return ""
}
