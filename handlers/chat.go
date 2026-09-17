package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ---- 客户端 AI 共享沟通室（聊天室仓库的 CHAT.md）----
// 逻辑从 ra_deploy/ops_console/server.py 的 chat_* 移植而来，规则见聊天室仓库 README。
// 关键点：发言前先 pull --rebase 同步主干；全程不使用 push --force（避免覆盖他人发言）；
// 并发撞号时把「自己那一块」的编号顺延为当前最大 +1 后重推。

const (
	// 提交作者**固定写死**：不依赖所在机器的 git 配置。
	// 47 上未设 user.name/user.email 时，git commit / rebase 会直接 fatal
	// 「Author identity unknown … unable to auto-detect email address」，导致发言整体失败。
	chatAuthorName  = "yakizkna"
	chatAuthorEmail = "yakizkna@outlook.com"
)

var (
	// 会话元数据识别 key：中文（正式「对话：」/兼容「会话：」）+ 英文（Conversation:/Session:）
	chatSessionKeys = []string{"- 对话：", "- 会话：", "- Conversation:", "- Session:"}
	// 时间锚点（用于识别发言块）：中英 2 种
	chatTimeKeys   = []string{"- 时间：", "- Time:"}
	chatTagRE      = regexp.MustCompile(`^Tag\.[A-Za-z0-9_-]{1,24}$`) // 内部规范 key（Tag.<短名>）
	chatTagShortRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,24}$`)      // 对话字段的短名
	chatReplyRE    = regexp.MustCompile(`^No\.\d+$`)
	// 「对话」字段里的回应编号：新语法 ReNo.<n>（与 No.<n> 同形）与旧写法 Re: No.<n> 都接受
	chatReNoRE = regexp.MustCompile(`(?:ReNo\.|Re:\s*No\.)(\d+)`)
	chatNoRE   = regexp.MustCompile(`No\.(\d+)`)
	// 发言块标题行 `# <发言人> No.<n>` —— 归档按它切块，不依赖 `---` 分隔线
	chatBlockHeadRE = regexp.MustCompile(`(?m)^# .*No\.(\d+)[ \t]*$`)
	chatNoTrailRE   = regexp.MustCompile(`\s*No\.\d+\s*$`)
)

type chatBlock struct {
	No      string // "No.57"
	Tag     string
	End     bool
	Reply   string
	Speaker string
}

// 对话字段语法（2026-09-17 用户定，统一到「解析 / 存储 / 显示」全链路）：
//
//	创建新 Tag：NewTag:<短名>
//	回复   Tag：Tag:<短名> ReNo.<n>      （ReNo 可省 ⇒ 该 Tag 的一般性发言，非回复某人）
//	带 Tag 发言：Tag:<短名>
//	结束   Tag：EndTag:<短名>            （可带 ReNo.<n>）
//
// 兼容旧写法：Tag.<短名> / Tag.<短名> Re: No.<n> / End: Tag.<短名>（历史条目一律不动）。
// parseChatSessionVal 把两种写法都归一到**内部规范** Tag（`Tag.<短名>`）+ End + Reply。
func parseChatSessionVal(val string) (tag string, end bool, reply string) {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "EndTag:") {
		end = true
		val = strings.TrimSpace(val[len("EndTag:"):])
	} else if strings.HasPrefix(val, "End:") {
		end = true
		val = strings.TrimSpace(val[len("End:"):])
	}
	if strings.HasPrefix(val, "NewTag:") {
		val = strings.TrimSpace(val[len("NewTag:"):])
	} else if strings.HasPrefix(val, "Tag:") {
		val = strings.TrimSpace(val[len("Tag:"):])
	}
	if m := chatReNoRE.FindStringSubmatch(val); m != nil {
		reply = "No." + m[1]
		val = strings.TrimSpace(val[:strings.Index(val, m[0])])
	}
	if val == "" {
		return "", end, reply
	}
	if !strings.HasPrefix(val, "Tag.") {
		val = "Tag." + val // 归一为内部规范 key
	}
	return val, end, reply
}

// ChatHandler 持有单个 chatroom 仓库路径，并提供读取 / 发言接口。
// 所有会改动工作区或远端的 git 操作都经 mu 串行化，避免并发 rebase/push 互相踩踏。
type ChatHandler struct {
	room string // 聊天室标识 = 仓库目录 basename（如 <chatroomA> / <chatroomB>）
	dir  string // 仓库根目录
	file string // CHAT.md 绝对路径
	mu   sync.Mutex
}

// ChatRooms 管理一组聊天室：CHATROOM_DIR 为逗号分隔的仓库目录列表，
// 每个目录的 **basename** 即聊天室 id。单房时缺省回退与历史一致。
type ChatRooms struct {
	rooms map[string]*ChatHandler // basename -> handler
	order []string                // 保持配置顺序（单房/首房即缺省）
}

func NewChatRooms() *ChatRooms {
	raw := strings.TrimSpace(os.Getenv("CHATROOM_DIR"))
	if raw == "" {
		raw = "/absolute/path/to/<chatroomA>" // 运行时默认：部署时按实际路径配置
	}
	cr := &ChatRooms{rooms: map[string]*ChatHandler{}}
	for _, part := range strings.Split(raw, ",") {
		dir := strings.TrimSpace(part)
		if dir == "" {
			continue
		}
		room := filepath.Base(filepath.Clean(dir))
		if room == "" || room == "." || room == string(filepath.Separator) {
			continue // 非法/根目录，跳过
		}
		if _, exists := cr.rooms[room]; !exists {
			cr.order = append(cr.order, room)
		}
		cr.rooms[room] = &ChatHandler{room: room, dir: dir, file: filepath.Join(dir, "CHAT.md")}
	}
	return cr
}

// resolve 按 room 名取 handler；room 为空时回退到首个聊天室（便于前端首次加载即可拿到内容+列表）。
func (cr *ChatRooms) resolve(room string) *ChatHandler {
	if room != "" {
		return cr.rooms[room]
	}
	if len(cr.order) > 0 {
		return cr.rooms[cr.order[0]]
	}
	return nil
}

// noAuthWhitelist 解析「免鉴权聊天室白名单」：CHATROOM_NOAUTH_WHITELIST 为逗号分隔的聊天室 id，
// 如 `<chatroomA>`, `<chatroomB>`。这些聊天室的 /api/chat/* 无需 JWT 登录即可读取/发言。
func (cr *ChatRooms) noAuthWhitelist() map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(os.Getenv("CHATROOM_NOAUTH_WHITELIST"), ",") {
		v := strings.TrimSpace(part)
		if v != "" {
			out[v] = true
		}
	}
	return out
}

// Auth 是 /api/chat/* 的 JWT 鉴权中间件：请求 `?room=<id>` 指向的聊天室若在
// CHATROOM_NOAUTH_WHITELIST 白名单里则直接放行（免登录）；否则走 RequireAdminJWT 管理端 JWT 鉴权。
// room 为空时按 resolve 的缺省规则取首个聊天室再判定，保证与后续实际读写同一房间保持一致。
func (cr *ChatRooms) Auth() gin.HandlerFunc {
	noauth := cr.noAuthWhitelist()
	jwt := RequireAdminJWT()
	return func(c *gin.Context) {
		if h := cr.resolve(c.Query("room")); h != nil && noauth[h.room] {
			c.Next() // 白名单聊天室：免鉴权放行
			return
		}
		jwt(c) // 其余走管理端 JWT
	}
}

// roomList 返回按配置顺序排列的聊天室列表，供前端下拉框渲染。
func (cr *ChatRooms) roomList() []gin.H {
	out := make([]gin.H, 0, len(cr.order))
	for _, id := range cr.order {
		out = append(out, gin.H{"id": id, "dir": cr.rooms[id].dir})
	}
	return out
}

// git 在当前聊天室仓库执行 git 命令，返回 (returncode, 合并后的输出)。
// 所有调用都强制带上作者身份：机器上没配 git 身份也能 commit / rebase。
func (h *ChatHandler) git(args ...string) (int, string) {
	full := make([]string, 0, len(args)+6)
	full = append(full, "-C", h.dir,
		"-c", "user.name="+chatAuthorName,
		"-c", "user.email="+chatAuthorEmail)
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	rc := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			rc = -1
		}
	}
	return rc, out.String()
}

// ---- 解析（与 Python _chat_files / _chat_blocks 对齐）----

func (h *ChatHandler) chatFiles() []string {
	var files []string
	if fi, err := os.Stat(h.file); err == nil && !fi.IsDir() {
		files = append(files, h.file)
	}
	entries, err := os.ReadDir(h.dir)
	if err == nil {
		for _, e := range entries {
			n := e.Name()
			if e.Type().IsRegular() && strings.HasPrefix(n, "CHAT_ARCHIVE_") && strings.HasSuffix(n, ".md") {
				files = append(files, filepath.Join(h.dir, n))
			}
		}
	}
	return files
}

func (h *ChatHandler) parseBlocks() []chatBlock {
	var blocks []chatBlock
	for _, path := range h.chatFiles() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(data)), "\n")
		inCode := false
		for i := 0; i < len(lines); i++ {
			ln := lines[i]
			if strings.HasPrefix(strings.TrimSpace(ln), "```") {
				inCode = !inCode
				continue
			}
			if inCode || !strings.HasPrefix(ln, "# ") {
				continue
			}
			// 标题下 1~3 个非空行内须出现时间锚点（- 时间：/- Time:）才算发言块
			head := []string{}
			for k := i + 1; k < len(lines) && len(head) < 3; k++ {
				if strings.TrimSpace(lines[k]) != "" {
					head = append(head, lines[k])
				}
			}
			hasTime := false
			for _, l := range head {
				for _, tk := range chatTimeKeys {
					if strings.HasPrefix(l, tk) {
						hasTime = true
						break
					}
				}
				if hasTime {
					break
				}
			}
			if !hasTime {
				continue
			}
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			meta := []string{}
			for j < len(lines) && strings.TrimSpace(lines[j]) != "" {
				meta = append(meta, strings.TrimSpace(lines[j]))
				j++
			}
			b := chatBlock{}
			b.Speaker = strings.TrimSpace(chatNoTrailRE.ReplaceAllString(strings.TrimSpace(ln[2:]), ""))
			if m := chatNoRE.FindStringSubmatch(ln); m != nil {
				b.No = "No." + m[1]
			}
			for _, mline := range meta {
				var key string
				for _, k := range chatSessionKeys {
					if strings.HasPrefix(mline, k) {
						key = k
						break
					}
				}
				if key == "" {
					continue
				}
				val := strings.TrimSpace(mline[len(key):])
				tag, end, reply := parseChatSessionVal(val) // 新语法 + 旧写法兼容
				b.Tag, b.End, b.Reply = tag, end, reply
			}
			blocks = append(blocks, b)
			i = j
		}
	}
	return blocks
}

func chatMaxNo(blocks []chatBlock) int {
	mx := 0
	for _, b := range blocks {
		if b.No == "" {
			continue
		}
		if n, err := strconv.Atoi(b.No[3:]); err == nil && n > mx {
			mx = n
		}
	}
	return mx
}

func chatTags(blocks []chatBlock) (known map[string]bool, closed map[string]bool) {
	known = map[string]bool{}
	closed = map[string]bool{}
	for _, b := range blocks {
		if b.Tag == "" {
			continue
		}
		known[b.Tag] = true
		if b.End {
			closed[b.Tag] = true
		}
	}
	return
}

func chatTagOwner(blocks []chatBlock, tag string) string {
	owner := ""
	best := -1
	for _, b := range blocks {
		if b.Tag != tag || b.No == "" {
			continue
		}
		if n, err := strconv.Atoi(b.No[3:]); err == nil && (best < 0 || n < best) {
			best = n
			owner = b.Speaker
		}
	}
	return owner
}

func chatReplyExists(blocks []chatBlock, reply string) bool {
	for _, b := range blocks {
		if b.No == reply {
			return true
		}
	}
	return false
}

func chatDupNo(blocks []chatBlock, no int) bool {
	target := "No." + strconv.Itoa(no)
	cnt := 0
	for _, b := range blocks {
		if b.No == target {
			cnt++
		}
	}
	return cnt > 1
}

// chatSessionLine 把「对话 / 回应」参数规范成元数据行；校验标签语法、End 权限与「End 后不可再引用」。
// 返回 (line, err, warn)：line 空且 err 非空＝拒绝；warn 非空＝照发但提示。
func chatSessionLine(speaker string, blocks []chatBlock, session, reply string, en, create bool) (string, string, string) {
	session = strings.TrimSpace(session)
	reply = strings.TrimSpace(reply)
	reply = regexp.MustCompile(`^Re:\s*`).ReplaceAllString(reply, "")
	if reply != "" && !strings.HasPrefix(reply, "No.") {
		reply = "No." + strings.TrimLeft(reply, "#")
	}
	if reply != "" && !chatReplyRE.MatchString(reply) {
		return "", "回应编号格式应为 No.<数字>（如 No.45）", ""
	}
	if session == "" {
		if reply != "" {
			return "", "回应编号（ReNo.<n>）必须与对话 Tag 同行 —— 写 `- 对话：Tag:<短名> ReNo.<n>`（指明回应哪条时请一并给出 Tag）", ""
		}
		return "", "", ""
	}
	// 对话字段语法（2026-09-17 用户定）：NewTag:<短名> / Tag:<短名>[ ReNo.<n>] / EndTag:<短名>[ ReNo.<n>]。
	// 入参一律接受四种写法：新语法、旧写法（End: Tag.<短名>）、带 Tag. 前缀、纯短名 —— 先归一到短名。
	end := false
	for _, pfx := range []string{"EndTag:", "End:"} {
		if strings.HasPrefix(session, pfx) {
			end = true
			session = strings.TrimSpace(session[len(pfx):])
			break
		}
	}
	for _, pfx := range []string{"NewTag:", "Tag:"} {
		if strings.HasPrefix(session, pfx) {
			session = strings.TrimSpace(session[len(pfx):])
			break
		}
	}
	if len(session) > 4 && strings.EqualFold(session[:4], "Tag.") { // 旧写法 Tag.<短名>
		session = strings.TrimSpace(session[4:])
	}
	if !chatTagShortRE.MatchString(session) {
		return "", "对话 Tag 格式：短名（字母/数字/-/_，≤24 字符）—— 新建 NewTag:<短名>，接续 Tag:<短名>[ ReNo.<n>]，结束 EndTag:<短名>；旧写法 Tag.<短名> / End: Tag.<短名> 亦兼容", ""
	}
	tag := "Tag." + session  // 内部规范 key（下文校验一律用它）
	disp := "Tag:" + session // 提示文案里给人看的显示形式
	known, closed := chatTags(blocks)
	// 2026-09-17 新规则：① 回复**必须**带 ReNo（不再允许「只带 Tag 的自由发言」）—— 仅对**已存在**的 Tag 生效，
	// 新建 Tag 时无对象可回复，允许不带；② 「创建 Tag」勾选时该短名必须不存在（互斥语义）。
	if create && known[tag] {
		return "", fmt.Sprintf("会话 %s 已存在，不能「创建 Tag」—— 请直接回复它（Tag:<短名> ReNo.<n>），或换一个短名", disp), ""
	}
	if !end && known[tag] && reply == "" {
		return "", fmt.Sprintf("会话 %s 已存在 —— 回复它必须带回应编号（Tag:<短名> ReNo.<n>，指明回应哪一条）", disp), ""
	}
	if end {
		if !known[tag] {
			return "", fmt.Sprintf("会话 %s 从未出现过，无需 EndTag（如只是想开新会话，请直接用 NewTag:<短名>）", disp), ""
		}
		if closed[tag] {
			return "", fmt.Sprintf("会话 %s 已是结束状态，无需重复 EndTag", disp), ""
		}
		owner := chatTagOwner(blocks, tag)
		if owner != "" && owner != speaker {
			return "", fmt.Sprintf("会话 %s 由「%s」发起 —— 规则 8 规定 **EndTag 只能由主题发起人** 使用，请让发起人来结束（或在其确认后代为操作）", disp, owner), ""
		}
	} else if closed[tag] {
		return "", fmt.Sprintf("会话 %s 已结束（EndTag）——已结束的 Tag 不可再引用，请另起一个新 Tag（README 核心规则 8）", disp), ""
	}
	if chatReplyRE.MatchString(reply) && !chatReplyExists(blocks, reply) {
		return "", fmt.Sprintf("回应目标 %s 不存在（房间里没有这个编号，先确认一下）", reply), ""
	}
	var warn string
	if !end {
		var others []string
		for t := range known {
			if !closed[t] && t != tag {
				others = append(others, "Tag:"+strings.TrimPrefix(t, "Tag."))
			}
		}
		if len(others) > 0 {
			warn = fmt.Sprintf("当前还有未结束的会话 %s —— 约定是「一个主题 End 之前不要开新主题」，建议先 EndTag 它再开新主题（本次已照发）", strings.Join(others, "、"))
		}
	}
	// 写入形态（统一语法）：首现 ⇒ NewTag:；End ⇒ EndTag:；其余 ⇒ Tag:；回应 ⇒ 追加 ReNo.<n>
	line := "- 对话："
	if en {
		line = "- Conversation: "
	}
	if end {
		line += "EndTag:" + session // 结束：EndTag:<短名>（可带 ReNo.<n>）
	} else {
		line += "Tag:" + session // 创建与回复同为 Tag:<短名>（回复必带 ReNo.<n>，见上方校验）
	}
	if reply != "" {
		line += " ReNo." + strings.TrimPrefix(reply, "No.")
	}
	return line, "", warn
}

func (h *ChatHandler) inRebase() bool {
	g := filepath.Join(h.dir, ".git")
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if fi, err := os.Stat(filepath.Join(g, d)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// resetToOrigin 把本地分支硬对齐到 origin/master（仅在我们自己的发言未落地时使用，绝不 force push）。
func (h *ChatHandler) resetToOrigin() bool {
	if rc, _ := h.git("fetch", "origin", "master"); rc != 0 {
		return false
	}
	if rc, _ := h.git("reset", "--hard", "origin/master"); rc != 0 {
		return false
	}
	return true
}

// writeBlock 把发言块插到 CHAT.md 最上方。
// 定位方式：以「当前标号最大的一块」为锚（新发言的 No 正是最大 +1），插到该块之前；
// 找不到任何块头（空文件等）时退回追加到末尾。
// 这样不依赖文件开头的空行/分隔线格式（曾因整理时去掉前导空行，`\n---\n` 首个匹配
// 落到第一块之后，新块被插成第二位置 ⇒ 置顶失效）。
func (h *ChatHandler) writeBlock(block string) (bool, string) {
	cur, err := os.ReadFile(h.file)
	if err != nil {
		return false, "读取 CHAT.md 失败：" + err.Error()
	}
	content := string(cur)
	idx := h.anchorTopInsertPos(content)
	var newContent string
	if idx >= 0 {
		newContent = content[:idx] + block + "\n" + strings.TrimLeft(content[idx:], "\n")
	} else {
		newContent = content + block
	}
	if err := os.WriteFile(h.file, []byte(newContent), 0644); err != nil {
		return false, "写入 CHAT.md 失败：" + err.Error()
	}
	return true, ""
}

// anchorTopInsertPos 返回「标号最大那一块」的开头分隔线 `\n---\n` 的位置（插到它之前即置顶），
// 与历史 writeBlock 的拼接口径一致（idx 指向分隔线前的换行）。
// 最大块在文件最顶且前导无换行时返回 0（插到文件开头）；找不到块头返回 -1。
func (h *ChatHandler) anchorTopInsertPos(content string) int {
	pos := -1
	maxN := -1
	for _, m := range chatBlockHeadRE.FindAllStringSubmatchIndex(content, -1) {
		n, err := strconv.Atoi(content[m[2]:m[3]])
		if err == nil && n > maxN {
			maxN = n
			pos = m[0] // 块头行行首偏移
		}
	}
	if pos < 0 {
		return -1
	}
	if idx := strings.LastIndex(content[:pos], "\n---\n"); idx >= 0 {
		return idx
	}
	return 0
}

// renumberTop 把写在最上方的那一块改号（撞号时用，只改自己那一块，不动他人内容）。
func (h *ChatHandler) renumberTop(oldNo, newNo int) (bool, string) {
	cur, err := os.ReadFile(h.file)
	if err != nil {
		return false, "读取 CHAT.md 失败：" + err.Error()
	}
	oldS, newS := strconv.Itoa(oldNo), strconv.Itoa(newNo)
	re := regexp.MustCompile(`(?m)^# (.+?) No\.` + regexp.QuoteMeta(oldS) + `$`)
	count := 0
	newCur := re.ReplaceAllStringFunc(string(cur), func(s string) string {
		count++
		m := re.FindStringSubmatch(s)
		return "# " + m[1] + " No." + newS
	})
	if count != 1 {
		return false, fmt.Sprintf("未找到待改号的发言块（No.%d）", oldNo)
	}
	if err := os.WriteFile(h.file, []byte(newCur), 0644); err != nil {
		return false, "写入 CHAT.md 失败：" + err.Error()
	}
	return true, ""
}

// chatBlockFromPart 把「被 \n---\n 切开的一段发言块正文」规范化成统一的块字符串：
// "\n---\n\n# ...\n"（先去首尾空白，避免反复归档时空白行无限累积）。
func chatBlockFromPart(part string) string {
	p := strings.TrimSpace(part)
	if p == "" {
		return ""
	}
	return "\n---\n\n" + p + "\n"
}

// archiveOverflow 把 CHAT.md 中超出最近 100 条的发言块归档到 CHAT_ARCHIVE_<k>.md。
// 约定见聊天室仓库 README「归档」一节：主文件只留最近 100 条；超出部分从最旧起搬走，
// 归档 _<k> 收纳 No.(100k-99)…No.(100k)；归档内保持「新→旧」（顶部最新），块内容原样保留。
// 返回本次改动到的归档文件相对名（供调用方一并 git add）；出错时第二个返回值非空。
func (h *ChatHandler) archiveOverflow() ([]string, string) {
	raw, err := os.ReadFile(h.file)
	if err != nil {
		return nil, "读取 CHAT.md 失败：" + err.Error()
	}
	// 统一换行为 \n 后**按块标题行（No.）切块**。
	// 不能按 "\n---\n" 切：历史文件存在 CRLF 混排，实测 102 个块只切出 92 段（漏 11 块），
	// 于是 len(blocks)<=100 一直成立、归档永不触发。
	data := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(raw))

	locs := chatBlockHeadRE.FindAllStringIndex(data, -1)
	if len(locs) <= 100 {
		return nil, ""
	}
	header := strings.TrimRight(data[:locs[0][0]], "\n") + "\n"
	// 每块 = 本块标题行起、到下一块标题行之前（含其后的分隔线/空白），文件里最新在最上
	blocks := make([]string, 0, len(locs))
	for i, l := range locs {
		end := len(data)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		blocks = append(blocks, data[l[0]:end])
	}
	kept := blocks[:100]     // 主文件保留的最近 100 条（最新在首）
	overflow := blocks[100:] // 超出的旧块

	byArch := map[int][]string{} // k -> 块正文（旧→新，与 CHAT.md 同序）
	var stay []string
	for _, body := range overflow {
		no := 0
		if m := chatNoRE.FindStringSubmatch(body); m != nil {
			if n, e := strconv.Atoi(m[1]); e == nil {
				no = n
			}
		}
		if no == 0 {
			stay = append(stay, body) // 无编号：保险起见留在主文件，不丢
			continue
		}
		k := (no-1)/100 + 1
		byArch[k] = append(byArch[k], body)
	}

	touched := []string{}
	for k, bodies := range byArch {
		fname := fmt.Sprintf("CHAT_ARCHIVE_%d.md", k)
		path := filepath.Join(h.dir, fname)
		existing := ""
		if d, e := os.ReadFile(path); e == nil {
			existing = string(d)
		}
		var sb strings.Builder
		for _, b := range bodies { // bodies 已为「新→旧」，正序写出即顶部最新
			sb.WriteString(chatBlockFromPart(b))
		}
		if err := os.WriteFile(path, []byte(sb.String()+existing), 0644); err != nil {
			return touched, "写入归档失败：" + err.Error()
		}
		touched = append(touched, fname)
	}

	var sb strings.Builder
	sb.WriteString(header)
	for _, b := range kept {
		sb.WriteString(chatBlockFromPart(b))
	}
	for _, b := range stay {
		sb.WriteString(chatBlockFromPart(b))
	}
	if err := os.WriteFile(h.file, []byte(sb.String()), 0644); err != nil {
		return touched, "回写 CHAT.md 失败：" + err.Error()
	}
	return touched, ""
}

func (h *ChatHandler) readChat() (string, string) {
	data, err := os.ReadFile(h.file)
	if err != nil {
		return "", "CHAT.md 尚未生成：" + h.file
	}
	return string(data), ""
}

// latestArchive 返回 `CHAT_ARCHIVE_<k>.md` 中 k 最大的一份（= 最新归档；没有则空串）。
// 归档编号越大越新（见聊天室仓库 README「归档」）⇒ 最大 k 正是「紧接 CHAT.md 之后的那一页」。
func (h *ChatHandler) latestArchive() string {
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		return ""
	}
	best, bestK := "", 0
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(n, "CHAT_ARCHIVE_") || !strings.HasSuffix(n, ".md") {
			continue
		}
		k, cerr := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(n, "CHAT_ARCHIVE_"), ".md"))
		if cerr != nil {
			continue
		}
		if k > bestK {
			bestK, best = k, n
		}
	}
	return best
}

// readArchive 返回最新一份归档的 (文件名, 全文)；沟通室页面把它接在 CHAT.md 之后展示，
// 于是**一次就能看到「最近 100 条 + 上一页 100 条」**（用户 2026-09-16 定：不用再翻归档文件）。
// 无归档或读取失败时返回 ("", "")（页面只显示 CHAT.md，不影响可用性）。
func (h *ChatHandler) readArchive() (string, string) {
	name := h.latestArchive()
	if name == "" {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join(h.dir, name))
	if err != nil {
		return "", ""
	}
	return name, strings.ReplaceAll(string(data), "\r\n", "\n")
}

func truncateRune(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Speak 把输入格式化后插入 CHAT.md 最上方，commit 并 push 到 origin/master。
// 返回 (ok, contentOrErr, warnings)。
func (h *ChatHandler) Speak(content, from, to, subject, session, reply, lang string, create bool) (bool, string, []string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, "内容为空", nil
	}
	// 元数据标签语言：en（英文）或空/其他（中文）。只本地化标签名，用户填写的值原样保留。
	en := strings.EqualFold(strings.TrimSpace(lang), "en")
	lab := func(zh, enS string) string {
		if en {
			return enS
		}
		return zh
	}
	if to = strings.TrimSpace(to); to == "" {
		if en {
			to = "everyone"
		} else {
			to = "所有人"
		}
	}
	lines := []string{}
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if subject = strings.TrimSpace(subject); subject == "" {
		if len(lines) > 0 {
			subject = truncateRune(lines[0], 40)
		} else {
			if en {
				subject = "post"
			} else {
				subject = "发言"
			}
		}
	}

	makeBlock := func() (string, int, string) {
		// 发言人 = 前端填写的「发件人」；不再有缺省发言人，为空则直接报错（发件人是必填）。
		speaker := strings.TrimSpace(from)
		if speaker == "" {
			return "", 0, "请填写发件人"
		}
		blocks := h.parseBlocks() // 只解析一次：解析要扫 CHAT.md + 全部归档，条数上百时开销明显
		sessionLine, err, warn := chatSessionLine(speaker, blocks, session, reply, en, create)
		if err != "" {
			return "", 0, err
		}
		no := chatMaxNo(blocks) + 1
		now := time.Now().UTC().Add(8 * time.Hour).Format("2006-01-02 15:04:05") // 北京时间（UTC+8）
		meta := []string{lab("- 时间：", "- Time: ") + now,
			lab("- 收件人：", "- To: ") + to,
			lab("- 主题：", "- Subject: ") + subject}
		if sessionLine != "" {
			meta = append(meta, sessionLine)
		}
		block := "\n---\n\n# " + speaker + " No." + strconv.Itoa(no) + "\n\n" +
			strings.Join(meta, "\n") + "\n\n" + content + "\n"
		return block, no, warn
	}

	// ① 清残留 rebase 态；工作区不干净直接拒绝（不替谁提交未知改动）
	if h.inRebase() {
		h.git("rebase", "--abort")
	}
	if rc, out := h.git("status", "--porcelain"); rc != 0 {
		return false, "git status 失败：" + out, nil
	} else if strings.TrimSpace(out) != "" {
		return false, "聊天室工作区有未提交改动，拒绝发言（请先提交或还原，避免与其它改动混淆）：\n" + out, nil
	}
	// ①b 本地不能领先 origin/master：否则冲突后 reset 会丢未推送提交
	if rc, out := h.git("fetch", "origin", "master"); rc != 0 {
		return false, "同步主干前 fetch origin/master 失败：" + out, nil
	}
	if rc, ahead := h.git("rev-list", "--count", "origin/master..HEAD"); rc != 0 {
		return false, "git rev-list 失败：" + ahead, nil
	} else if strings.TrimSpace(ahead) != "" && strings.TrimSpace(ahead) != "0" {
		return false, fmt.Sprintf("聊天室本地有 %s 个未推送提交，拒绝发言（以免后续同步时被 reset 丢弃）：请先自行 push 或还原后再发", strings.TrimSpace(ahead)), nil
	}

	// 本次发言一旦改动过工作区（writeBlock / 归档之后），其后任何一步失败都必须回滚：
	// 否则 CHAT.md 停在「已改动未提交」状态，① 的工作区守卫会拒绝之后所有发言 ⇒ 聊天室卡死
	// （47 上 git commit 因缺身份失败时实测踩到：块已写入、commit 却 fatal）。
	dirty := false
	touchedArch := []string{} // 本次归档到的文件（回滚时若属新建的未跟踪文件要一并删）
	rollback := func() {
		h.resetToOrigin()
		// reset 不删未跟踪文件：归档文件若是本次新建的（未跟踪）同样会让 ① 的守卫一直拒绝发言。
		for _, f := range touchedArch {
			if rc, _ := h.git("ls-files", "--error-unmatch", f); rc != 0 {
				_ = os.Remove(filepath.Join(h.dir, f))
			}
		}
	}
	defer func() {
		if dirty {
			rollback()
		}
	}()

	var lastErr string
	var warns []string
	committed := false
	block := ""
	no := 0
	// ②③④：最多 3 轮（正常 1 轮；push 被拒 1 轮；与远端发言冲突后重做 1 轮）
	for i := 0; i < 3; i++ {
		rc, out := h.git("pull", "--rebase", "origin", "master")
		if rc != 0 {
			h.git("rebase", "--abort")
			if !h.resetToOrigin() {
				return false, "同步主干失败（pull --rebase，已 abort，未发言）：" + out, nil
			}
			committed = false
			lastErr = out
			continue
		}
		if !committed {
			var errStr, warn string
			block, no, errStr = makeBlock()
			if errStr != "" {
				return false, errStr, nil
			}
			if warn != "" {
				warns = append(warns, warn)
			}
			ok, werr := h.writeBlock(block)
			if !ok {
				return false, werr, nil
			}
			dirty = true // 已落盘：此后失败一律回滚
			archives, aerr := h.archiveOverflow()
			if aerr != "" {
				return false, aerr, nil
			}
			touchedArch = append(touchedArch, archives...)
			// 规则已固化为通用规则（前端静态文本），不再依赖各仓库 README.md：
			// 只 add CHAT.md + 归档（新仓库没有 README.md 也不会让 git add 报错）。
			addArgs := []string{"add", "CHAT.md"}
			for _, f := range archives {
				addArgs = append(addArgs, f)
			}
			if rc, out := h.git(addArgs...); rc != 0 {
				return false, "git add 失败：" + out, nil
			}
			if rc, out := h.git("commit", "-m", fmt.Sprintf("chat No.%d: %s", no, truncateRune(subject, 60))); rc != 0 {
				return false, "git commit 失败：" + out, nil
			}
			committed = true
		} else if chatDupNo(h.parseBlocks(), no) {
			// 撞号（README 规则 7）：把自己的号顺延为当前最大 +1（只改自己那一块），改完重推
			newNo := chatMaxNo(h.parseBlocks()) + 1
			ok, rerr := h.renumberTop(no, newNo)
			if !ok {
				return false, rerr, nil
			}
			h.git("add", "CHAT.md")
			if rc, out := h.git("commit", "--amend", "--no-edit"); rc != 0 {
				return false, "撞号改号后 commit --amend 失败：" + out, nil
			}
			no = newNo
		}
		rc, out = h.git("push", "origin", "master")
		if rc == 0 {
			content, rerr := h.readChat()
			if rerr != "" {
				content = ""
			}
			dirty = false // 已成功上远端，不需要回滚
			return true, content, warns
		}
		lastErr = out
		// push 失败：回滚本次本地提交。否则本地会一直「领先 origin」，下一条 ①b 守卫
		// 直接拒绝发言 ⇒ 聊天室卡死且无人知道要怎么恢复。回滚只丢弃自己刚 commit 的那一块
		// （尚未上远端），不触碰他人内容；下一轮会重新取号、重新生成块再试。
		if committed {
			rollback()
			dirty = false
			committed = false
		}
	}
	return false, "推送失败（已重试，全程未使用 force）：" + lastErr, nil
}

// ---- HTTP 接口（均经 RequireAdminJWT 鉴权）----

// GetChat GET /api/chat?room=<id> —— 同步主干后读取所选聊天室 CHAT.md 全文。
func (cr *ChatRooms) GetChat(c *gin.Context) {
	h := cr.resolve(c.Query("room"))
	if h == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "未知聊天室：" + c.Query("room") + "（CHATROOM_DIR 配置了多个仓库时请用 ?room=<目录名> 选择）"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if rc, out := h.git("pull", "--rebase", "origin", "master"); rc != 0 {
		// 同步失败不致命：仍读本地
		_ = out
	}
	content, err := h.readChat()
	if err != "" {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err})
		return
	}
	// 同时给出「最新一份归档」的全文：页面把两页拼在一起渲染，省去人工翻归档。
	archiveName, archive := h.readArchive()
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"content":     content,
		"archive":     archive,
		"archiveName": archiveName,
		"repo":        "yakizkna/" + h.room,
		"chatroomDir": h.dir,
		"room":        h.room,
		"rooms":       cr.roomList(),
		"noauth":      cr.noAuthWhitelist()[h.room], // 当前房是否在免鉴权白名单（前端据此隐藏登出）
	})
}

// Update POST /api/chat/update?room=<id> —— 同步所选聊天室主干并返回最新内容（只读刷新）。
func (cr *ChatRooms) Update(c *gin.Context) {
	h := cr.resolve(c.Query("room"))
	if h == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "未知聊天室：" + c.Query("room")})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rc, out := h.git("pull", "--rebase", "origin", "master")
	content, err := h.readChat()
	if err != "" {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err})
		return
	}
	note := out
	if rc != 0 {
		note = "pull 失败：" + out
	}
	archiveName, archive := h.readArchive()
	c.JSON(http.StatusOK, gin.H{"ok": true, "content": content, "note": note,
		"archive": archive, "archiveName": archiveName, "room": h.room, "rooms": cr.roomList()})
}

// Speak POST /api/chat/speak?room=<id> —— 向所选聊天室发言（写 CHAT.md 并 push）。
func (cr *ChatRooms) SpeakHTTP(c *gin.Context) {
	var req struct {
		Content, From, To, Subject, Session, Reply, Lang string
		Create                                           bool // 「创建 Tag」勾选（该短名必须不存在）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "invalid request"})
		return
	}
	h := cr.resolve(c.Query("room"))
	if h == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "未知聊天室：" + c.Query("room")})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ok, contentOrErr, warns := h.Speak(req.Content, req.From, req.To, req.Subject, req.Session, req.Reply, req.Lang, req.Create)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": contentOrErr})
		return
	}
	note := "已发言，并 push 到 origin/master"
	if len(warns) > 0 {
		note += "　⚠ " + strings.Join(warns, "；")
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "content": contentOrErr, "note": note})
}

// ChatFile GET /api/chat/file/*filepath?room=<id> —— 代理读取所选聊天室仓库内的文件，
// 替代原先「浏览器直连 raw.githubusercontent.com」的做法：即使仓库是私有仓库，
// 只要已登录（JWT）即可经本代理访问，无需把仓库设为公开。读取范围限定在该仓库目录之内（防目录穿越）。
func (cr *ChatRooms) ChatFile(c *gin.Context) {
	h := cr.resolve(c.Query("room"))
	if h == nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "未知聊天室：" + c.Query("room")})
		return
	}
	rel := strings.TrimPrefix(c.Param("filepath"), "/")
	rel = filepath.Clean("/" + rel)[1:] // 规范化，消解任何 ../ 穿越（绝对化后再取相对部分）
	if rel == "" || rel == "." {
		c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "missing path"})
		return
	}
	// 屏蔽点开头的路径段（.git / .github / .env 等）：否则 /api/chat/file/.git/config 会把
	// 私有仓库的 remote URL 交出去 —— 其中若带 token，等于泄露本仓库写权限。
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "not found"})
			return
		}
	}
	full := filepath.Join(h.dir, rel)
	base := filepath.Clean(h.dir)
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		c.JSON(http.StatusForbidden, gin.H{"ok": false, "error": "forbidden"})
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "not found"})
		return
	}
	c.File(full)
}
