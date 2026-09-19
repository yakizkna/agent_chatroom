package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 对话字段语法（2026-09-17 用户定，统一到解析 / 存储 / 显示）：
//
//	创建 Tag：Tag:<短名>                 （首现即创建；勾「创建Tag」时短名必须不存在）
//	回复 Tag：Tag:<短名> ReNo:<n>        （对**已存在**的 Tag，ReNo **必填** —— 不再允许只带 Tag 的自由发言）
//	结束 Tag：EndTag:<短名> [ReNo:<n>]   （ReNo 可省）
//
// 旧写法（Tag.<短名> / End: Tag.<短名> / Re: No.<n>）一律兼容，历史条目不改写。
func testBlocks() []chatBlock {
	return []chatBlock{
		{No: "No.10", Tag: "Tag.cup-quota-0917", Speaker: "ra_agent"},
		{No: "No.11", Tag: "Tag.cup-quota-0917", Speaker: "棒球龙虾"},
		{No: "No.12", Tag: "Tag.old-topic", End: true, Speaker: "yaki"},
	}
}

func TestParseChatSessionVal(t *testing.T) {
	cases := []struct {
		in, tag, reply string
		end            bool
		why            string
	}{
		{"Tag:cup-quota-0917", "Tag.cup-quota-0917", "", false, "新语法·创建/一般"},
		{"Tag:cup-quota-0917 ReNo:144", "Tag.cup-quota-0917", "No.144", false, "新语法·回复"},
		{"EndTag:cup-quota-0917", "Tag.cup-quota-0917", "", true, "新语法·结束（不带 ReNo）"},
		{"EndTag:cup-quota-0917 ReNo:144", "Tag.cup-quota-0917", "No.144", true, "新语法·结束（带 ReNo）"},
		{"NewTag:cup-quota-0917", "Tag.cup-quota-0917", "", false, "过渡写法 NewTag: 仍被接受（等同 Tag:）"},
		{"Tag:cup-quota-0917 ReNo.144", "Tag.cup-quota-0917", "No.144", false, "旧的**点**写法 ReNo.<n> 仍兼容"},
		{"Tag.cup-quota-0917", "Tag.cup-quota-0917", "", false, "旧写法·一般"},
		{"Tag.cup-quota-0917 Re: No.144", "Tag.cup-quota-0917", "No.144", false, "旧写法·回复"},
		{"End: Tag.cup-quota-0917", "Tag.cup-quota-0917", "", true, "旧写法·结束"},
		{"", "", "", false, "空值"},
	}
	for _, c := range cases {
		tag, end, reply := parseChatSessionVal(c.in)
		if tag != c.tag || end != c.end || reply != c.reply {
			t.Errorf("%s：parse(%q) = (%q, %v, %q)，期望 (%q, %v, %q)",
				c.why, c.in, tag, end, reply, c.tag, c.end, c.reply)
		}
	}
}

func TestChatSessionLineWritesUnifiedSyntax(t *testing.T) {
	b := testBlocks()
	cases := []struct {
		session, reply string
		en, create     bool
		want, why      string
	}{
		{"cup-quota-0917", "11", false, false, "- 对话：Tag:cup-quota-0917 ReNo:11", "已存在的 Tag + 回复 ⇒ Tag:… ReNo:<n>"},
		{"cup-quota-0917", "#11", false, false, "- 对话：Tag:cup-quota-0917 ReNo:11", "回应编号可省 No. 前缀"},
		{"Tag.cup-quota-0917", "11", false, false, "- 对话：Tag:cup-quota-0917 ReNo:11", "旧写法前缀兼容"},
		{"new-topic", "", false, true, "- 对话：Tag:new-topic", "创建 Tag（无 ReNo）⇒ Tag:<短名>"},
		{"new-topic", "11", false, false, "- 对话：Tag:new-topic ReNo:11", "新 Tag 也允许直接带 ReNo"},
		{"EndTag:cup-quota-0917", "", false, false, "- 对话：EndTag:cup-quota-0917", "结束（不带 ReNo）"},
		{"EndTag:cup-quota-0917", "11", false, false, "- 对话：EndTag:cup-quota-0917 ReNo:11", "结束（带 ReNo）"},
		{"End: Tag.cup-quota-0917", "", false, false, "- 对话：EndTag:cup-quota-0917", "旧写法结束 ⇒ EndTag:"},
		{"new-topic", "", true, true, "- Conversation: Tag:new-topic", "英文元数据行同样用统一语法"},
	}
	for _, c := range cases {
		got, errMsg, _ := chatSessionLine("ra_agent", b, c.session, c.reply, c.en, c.create)
		if errMsg != "" {
			t.Errorf("%s：session=%q 被拒：%s", c.why, c.session, errMsg)
			continue
		}
		if got != c.want {
			t.Errorf("%s：session=%q ⇒ %q，期望 %q", c.why, c.session, got, c.want)
		}
	}
}

func TestChatSessionLineStillValidates(t *testing.T) {
	b := testBlocks()
	bad := []struct {
		session, reply string
		create         bool
		why            string
	}{
		{"cup-quota-0917", "", false, "已存在的 Tag 不带 ReNo（不再允许自由发言）应被拒"},
		{"cup-quota-0917", "", true, "对已存在的 Tag 勾「创建 Tag」应被拒（互斥语义）"},
		{"cup quota", "11", false, "含空格的短名应被拒"},
		{"", "11", false, "只给回应编号而不给 Tag 应被拒"},
		{"cup-quota-0917", "999", false, "回应不存在的编号应被拒"},
		{"EndTag:never-seen", "", false, "EndTag 一个从未出现的 Tag 应被拒"},
		{"EndTag:old-topic", "", false, "重复 EndTag 应被拒"},
	}
	for _, c := range bad {
		if _, errMsg, _ := chatSessionLine("ra_agent", b, c.session, c.reply, false, c.create); errMsg == "" {
			t.Errorf("%s（session=%q）", c.why, c.session)
		}
	}
	if _, errMsg, _ := chatSessionLine("棒Buddy", b, "EndTag:cup-quota-0917", "", false, false); errMsg == "" {
		t.Errorf("非发起人 EndTag 应被拒（发起人 = 该 Tag 最早一条的发言人）")
	}
	if _, errMsg, _ := chatSessionLine("棒Buddy", b, "old-topic", "11", false, false); errMsg == "" {
		t.Errorf("向已结束的 Tag 发言应被拒")
	}
}

// 块间分隔线固定为 chatSepLines（=2）条（2026-09-18 用户定）。
// 历史成因：每次发言各留一条、从不合并 ⇒ 累积成 0~9 条 ⇒ 页面虚线数量飘忽。
// 注意「块正文里的分隔线（不是块尾填充）必须原样保留」，否则会毁掉发言内容。
func TestNormalizeChatSeps(t *testing.T) {
	messy := strings.Join([]string{
		"",
		"# A No.2",
		"",
		"- 时间：2026-09-18 00:00:00",
		"",
		"正文里有一条真的分隔线（须保留）：",
		"",
		"---",
		"",
		"尾行",
		"",
		"---",
		"",
		"---",
		"",
		"---",
		"",
		"# B No.1",
		"",
		"- 时间：2026-09-17 23:00:00",
		"",
		"只有一条分隔线",
		"",
		"---",
		"",
		"",
	}, "\n")
	want := strings.Join([]string{
		"# A No.2",
		"",
		"- 时间：2026-09-18 00:00:00",
		"",
		"正文里有一条真的分隔线（须保留）：",
		"",
		"---",
		"",
		"尾行",
		"",
		"---",
		"",
		"---",
		"",
		"# B No.1",
		"",
		"- 时间：2026-09-17 23:00:00",
		"",
		"只有一条分隔线",
		"", // 最后一块之后同样固定 2 条（「每次发言后」含最后一条）
		"---",
		"",
		"---",
	}, "\n")
	got := normalizeChatSeps(messy)
	if got != want {
		t.Errorf("整理结果不符：\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if again := normalizeChatSeps(got); again != got {
		t.Errorf("整理应幂等，二次结果变了：\n%s", again)
	}
	if regexp.MustCompile(`---\n\n---\n\n---`).MatchString(got) {
		t.Errorf("仍存在 3 条以上连续分隔线：\n%s", got)
	}
	if n := len(chatBlockHeadRE.FindAllString(got, -1)); n != 2 {
		t.Errorf("块数应保持 2，实际 %d", n)
	}
}

// ArchiveIfNeeded（ra_tasks 调用的命令行归档入口）：≤200 条必须**零副作用**（不写文件、不碰 git），
// `--dry` 只报告且不得落盘。真正搬块/提交的部分由 TestArchiveOverflowBatches + 端到端夹具覆盖。
func TestArchiveIfNeededNoopAndDry(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "CHAT.md")
	mk := func(hi int) string {
		var sb strings.Builder
		for n := hi; n >= 1; n-- {
			fmt.Fprintf(&sb, "# T No.%d\n\n- 时间：2026-09-18 00:00:00\n- 收件人：所有人\n- 主题：t\n\nb\n\n---\n\n---\n\n", n)
		}
		return sb.String()
	}
	h := NewChatHandler(dir)

	// 150 条 ⇒ 无需归档（且不应因为目录不是 git 仓库而报错）
	if err := os.WriteFile(file, []byte(mk(150)), 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	line, err := h.ArchiveIfNeeded(false)
	if err != nil || !strings.Contains(line, "无需归档") {
		t.Errorf("150 条应无需归档：line=%q err=%v", line, err)
	}
	if line2, err2 := h.ArchiveIfNeeded(true); err2 != nil || !strings.Contains(line2, "无需归档") {
		t.Errorf("dry 版同样应无需归档：%q %v", line2, err2)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Errorf("无需归档时不应改写 CHAT.md")
	}

	// 210 条 ⇒ dry 预报「搬走最旧 110 条、回落 100 条」，且**不落盘、不产生归档文件**
	if err := os.WriteFile(file, []byte(mk(210)), 0644); err != nil {
		t.Fatal(err)
	}
	before2, _ := os.ReadFile(file)
	line3, err3 := h.ArchiveIfNeeded(true)
	if err3 != nil {
		t.Fatalf("dry 不应报错：%v", err3)
	}
	if !strings.Contains(line3, "将搬走最旧 110 条") || !strings.Contains(line3, "回落到 100 条") {
		t.Errorf("dry 预报不符：%q", line3)
	}
	if after2, _ := os.ReadFile(file); string(after2) != string(before2) {
		t.Errorf("dry 不应改写 CHAT.md")
	}
	if _, e := os.Stat(filepath.Join(dir, "CHAT_ARCHIVE_1.md")); e == nil {
		t.Errorf("dry 不应产生归档文件")
	}
}

// 归档改「批量」（2026-09-18 用户定）：`CHAT.md` 最多 200 条，**到达时一次搬走最旧的 ≈100 条**。
// 关键回归：**101~200 条之间一律不动**（旧实现在 101 条就搬 1 条 ⇒ **每发一贴都要重写归档**）。
func TestArchiveOverflowBatches(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "CHAT.md")
	h := &ChatHandler{room: "t", dir: dir, file: file}

	mk := func(hi, lo int) string { // 生成块（新 → 旧：hi 最新），块头形如 `# T No.<n>`
		var sb strings.Builder
		for n := hi; n >= lo; n-- {
			fmt.Fprintf(&sb, "# T No.%d\n\n- 时间：2026-09-18 00:00:00\n- 收件人：所有人\n- 主题：t%d\n\nbody %d\n\n---\n\n---\n\n", n, n, n)
		}
		return sb.String()
	}
	count := func(p string) int {
		b, err := os.ReadFile(p)
		if err != nil {
			return -1
		}
		return len(chatBlockHeadRE.FindAllString(string(b), -1))
	}
	// span 返回归档/主文件的「条数 + 编号区间」（批量后 `_<k>` 仍应按 No. 区间对齐）
	span := func(p string) (int, int, int) {
		b, err := os.ReadFile(p)
		if err != nil {
			return -1, -1, -1
		}
		var ns []int
		for _, m := range regexp.MustCompile(`No\.(\d+)`).FindAllStringSubmatch(string(b), -1) {
			var n int
			fmt.Sscanf(m[1], "%d", &n)
			ns = append(ns, n)
		}
		if len(ns) == 0 {
			return 0, 0, 0
		}
		lo, hi := ns[0], ns[0]
		for _, n := range ns {
			if n < lo {
				lo = n
			}
			if n > hi {
				hi = n
			}
		}
		return len(ns), lo, hi
	}
	arch1, arch2, arch3 := filepath.Join(dir, "CHAT_ARCHIVE_1.md"), filepath.Join(dir, "CHAT_ARCHIVE_2.md"), filepath.Join(dir, "CHAT_ARCHIVE_3.md")

	// ① 201 条 ⇒ 触发批量：保留最新 100（No.102–201），一次搬走 101 条（No.1–100 → _1、No.101 → _2）
	if err := os.WriteFile(file, []byte(mk(201, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	touched, msg := h.archiveOverflow()
	if msg != "" {
		t.Fatalf("归档报错：%s", msg)
	}
	if len(touched) == 0 {
		t.Fatal("201 条时应触发批量归档，实际没动")
	}
	if n, lo, hi := span(file); n != 100 || lo != 102 || hi != 201 {
		t.Errorf("主文件应保留 100 条（No.102–201），实际 %d 条（No.%d–%d）", n, lo, hi)
	}
	if n, lo, hi := span(arch1); n != 100 || lo != 1 || hi != 100 {
		t.Errorf("_1 应 100 条（No.1–100），实际 %d 条（No.%d–%d）", n, lo, hi)
	}
	if n, lo, hi := span(arch2); n != 1 || lo != 101 || hi != 101 {
		t.Errorf("_2 应 1 条（No.101），实际 %d 条（No.%d–%d）", n, lo, hi)
	}
	kept, _ := os.ReadFile(file)
	if !strings.Contains(string(kept), "No.201") || strings.Contains(string(kept), "No.101\n") {
		t.Errorf("保留的应是 No.102–201（边界不符）")
	}

	// ② 101 条（相当于新发 1 贴）⇒ **不触发**（这就是「不再每贴重写归档」的保证）
	one, _ := os.ReadFile(file)
	if err := os.WriteFile(file, []byte(mk(202, 202)+string(one)), 0644); err != nil {
		t.Fatal(err)
	}
	a1before, _ := os.ReadFile(arch1)
	if touched, msg = h.archiveOverflow(); msg != "" || touched != nil {
		t.Errorf("101 条不应触发归档，实际 touched=%v msg=%q", touched, msg)
	}
	if a1after, _ := os.ReadFile(arch1); string(a1after) != string(a1before) {
		t.Errorf("101 条时归档不应被改写")
	}
	if n := count(file); n != 101 {
		t.Errorf("应保持 101 条，实际 %d", n)
	}

	// ③ 再加 100 条（共 201）⇒ 再触发一次批量：主文件回落 100，_1 保持不动，_2 收 No.101–202
	cur, _ := os.ReadFile(file)
	if err := os.WriteFile(file, []byte(mk(302, 203)+string(cur)), 0644); err != nil {
		t.Fatal(err)
	}
	if touched, msg = h.archiveOverflow(); msg != "" || len(touched) == 0 {
		t.Fatalf("201 条应再次触发批量，实际 touched=%v msg=%q", touched, msg)
	}
	if n, lo, hi := span(file); n != 100 || lo != 203 || hi != 302 {
		t.Errorf("主文件应再次回落到 100 条（No.203–302），实际 %d 条（No.%d–%d）", n, lo, hi)
	}
	if n, lo, hi := span(arch2); n != 100 || lo != 101 || hi != 200 {
		t.Errorf("_2 应补满为 100 条（No.101–200），实际 %d 条（No.%d–%d）", n, lo, hi)
	}
	if n, lo, hi := span(arch3); n != 2 || lo != 201 || hi != 202 {
		t.Errorf("_3 应收 2 条（No.201–202），实际 %d 条（No.%d–%d）", n, lo, hi)
	}
	if a1after, _ := os.ReadFile(arch1); string(a1after) != string(a1before) {
		t.Errorf("_1 已满，不应再被改写（批量归档的意义）")
	}
}

// 历史视图（2026-09-19 加）：归档清单（k 降序 + No. 区间）+ 按名读取（白名单，防目录穿越）。
func TestListArchivesAndArchiveByName(t *testing.T) {
	dir := t.TempDir()
	block := func(no, txt string) string {
		return fmt.Sprintf("# 甲 No.%s\n\n- 时间：2026-09-19 10:00:00\n- 收件人：所有人\n- 主题：%s\n\n%s\n\n---\n\n---\n", no, txt, txt)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写 %s 失败：%v", name, err)
		}
	}
	write("CHAT.md", block("201", "最新一贴"))
	write("CHAT_ARCHIVE_1.md", block("1", "最早一贴")+block("100", "第一页末"))
	write("CHAT_ARCHIVE_2.md", block("101", "第二页首"))
	write("CHAT_ARCHIVE_x.md", block("9", "文件名非法，不该被列出"))
	write("OTHER.md", "无关文件")

	h := NewChatHandler(dir)
	list := h.listArchives()
	if len(list) != 2 {
		t.Fatalf("归档清单应 2 项（只认 CHAT_ARCHIVE_<数字>.md），实际 %d 项：%v", len(list), list)
	}
	if list[0]["name"] != "CHAT_ARCHIVE_2.md" {
		t.Errorf("清单应 k 降序（新的在前），首项 = %v", list[0]["name"])
	}
	if list[0]["from"] != 101 || list[0]["to"] != 101 {
		t.Errorf("_2 的 No. 区间应 101–101，实际 %v–%v", list[0]["from"], list[0]["to"])
	}
	if list[1]["blocks"] != 2 || list[1]["from"] != 1 || list[1]["to"] != 100 {
		t.Errorf("_1 应 2 块、No.1–100，实际 blocks=%v No.%v–%v", list[1]["blocks"], list[1]["from"], list[1]["to"])
	}

	if c, ok := h.archiveByName("CHAT_ARCHIVE_1.md"); !ok || !strings.Contains(c, "第一页末") {
		t.Errorf("按名读取 _1 应成功且含其内容，ok=%v content=%q", ok, c)
	}
	for _, bad := range []string{"../CHAT.md", "CHAT.md", "CHAT_ARCHIVE_x.md", "CHAT_ARCHIVE_9.md", "CHAT_ARCHIVE_1.md.bak"} {
		if _, ok := h.archiveByName(bad); ok {
			t.Errorf("%q 不该被接受（白名单外或不存在）", bad)
		}
	}
}
