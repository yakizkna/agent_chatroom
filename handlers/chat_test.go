package handlers

import "testing"

// 对话字段语法（2026-09-17 用户定，统一到解析 / 存储 / 显示）：
//
//	创建 Tag：Tag:<短名>                 （首现即创建；勾「创建Tag」时短名必须不存在）
//	回复 Tag：Tag:<短名> ReNo.<n>        （对**已存在**的 Tag，ReNo **必填** —— 不再允许只带 Tag 的自由发言）
//	结束 Tag：EndTag:<短名> [ReNo.<n>]   （ReNo 可省）
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
		{"Tag:cup-quota-0917 ReNo.144", "Tag.cup-quota-0917", "No.144", false, "新语法·回复"},
		{"EndTag:cup-quota-0917", "Tag.cup-quota-0917", "", true, "新语法·结束（不带 ReNo）"},
		{"EndTag:cup-quota-0917 ReNo.144", "Tag.cup-quota-0917", "No.144", true, "新语法·结束（带 ReNo）"},
		{"NewTag:cup-quota-0917", "Tag.cup-quota-0917", "", false, "过渡写法 NewTag: 仍被接受（等同 Tag:）"},
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
		{"cup-quota-0917", "11", false, false, "- 对话：Tag:cup-quota-0917 ReNo.11", "已存在的 Tag + 回复 ⇒ Tag:… ReNo.<n>"},
		{"cup-quota-0917", "#11", false, false, "- 对话：Tag:cup-quota-0917 ReNo.11", "回应编号可省 No. 前缀"},
		{"Tag.cup-quota-0917", "11", false, false, "- 对话：Tag:cup-quota-0917 ReNo.11", "旧写法前缀兼容"},
		{"new-topic", "", false, true, "- 对话：Tag:new-topic", "创建 Tag（无 ReNo）⇒ Tag:<短名>"},
		{"new-topic", "11", false, false, "- 对话：Tag:new-topic ReNo.11", "新 Tag 也允许直接带 ReNo"},
		{"EndTag:cup-quota-0917", "", false, false, "- 对话：EndTag:cup-quota-0917", "结束（不带 ReNo）"},
		{"EndTag:cup-quota-0917", "11", false, false, "- 对话：EndTag:cup-quota-0917 ReNo.11", "结束（带 ReNo）"},
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
