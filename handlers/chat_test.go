package handlers

import "testing"

// 对话字段语法（2026-09-17 用户定，统一到解析 / 存储 / 显示）：
//
//	创建新 Tag：NewTag:<短名>
//	回复 Tag：  Tag:<短名> ReNo.<n>（ReNo 可省 ⇒ 该 Tag 的一般性发言）
//	结束 Tag：  EndTag:<短名>
//
// 旧写法（Tag.<短名> / End: Tag.<短名> / Re: No.<n>）一律兼容，且**历史条目不改写**。
func testBlocks() []chatBlock {
	return []chatBlock{
		{No: "No.10", Tag: "Tag.cup-quota-0917", Speaker: "ra_agent"},
		{No: "No.11", Tag: "Tag.cup-quota-0917", Speaker: "棒球龙虾"},
		{No: "No.12", Tag: "Tag.old-topic", End: true, Speaker: "yaki"},
	}
}

// parseChatSessionVal：读路径把新旧写法统一归一到内部规范 key。
func TestParseChatSessionVal(t *testing.T) {
	cases := []struct {
		in    string
		tag   string
		end   bool
		reply string
		why   string
	}{
		{"NewTag:cup-quota-0917", "Tag.cup-quota-0917", false, "", "新语法·新建"},
		{"Tag:cup-quota-0917", "Tag.cup-quota-0917", false, "", "新语法·一般发言"},
		{"Tag:cup-quota-0917 ReNo.144", "Tag.cup-quota-0917", false, "No.144", "新语法·回复"},
		{"EndTag:cup-quota-0917", "Tag.cup-quota-0917", true, "", "新语法·结束"},
		{"EndTag:cup-quota-0917 ReNo.144", "Tag.cup-quota-0917", true, "No.144", "新语法·结束+回复"},
		{"Tag.cup-quota-0917", "Tag.cup-quota-0917", false, "", "旧写法·一般发言"},
		{"Tag.cup-quota-0917 Re: No.144", "Tag.cup-quota-0917", false, "No.144", "旧写法·回复"},
		{"End: Tag.cup-quota-0917", "Tag.cup-quota-0917", true, "", "旧写法·结束"},
		{"", "", false, "", "空值"},
	}
	for _, c := range cases {
		tag, end, reply := parseChatSessionVal(c.in)
		if tag != c.tag || end != c.end || reply != c.reply {
			t.Errorf("%s：parse(%q) = (%q, %v, %q)，期望 (%q, %v, %q)",
				c.why, c.in, tag, end, reply, c.tag, c.end, c.reply)
		}
	}
}

// chatSessionLine：写路径按统一语法落库（首现 NewTag:、结束 EndTag:、其余 Tag:、回应追加 ReNo.<n>）。
func TestChatSessionLineWritesUnifiedSyntax(t *testing.T) {
	b := testBlocks()
	cases := []struct {
		session, reply string
		en             bool
		want, why      string
	}{
		{"cup-quota-0917", "", false, "- 对话：Tag:cup-quota-0917", "已存在的 Tag ⇒ Tag:"},
		{"cup-quota-0917", "11", false, "- 对话：Tag:cup-quota-0917 ReNo.11", "回复 ⇒ 追加 ReNo.<n>"},
		{"cup-quota-0917", "#11", false, "- 对话：Tag:cup-quota-0917 ReNo.11", "回应编号可省 No. 前缀"},
		{"Tag.cup-quota-0917", "", false, "- 对话：Tag:cup-quota-0917", "旧写法前缀兼容"},
		{"Tag:cup-quota-0917", "", false, "- 对话：Tag:cup-quota-0917", "新语法前缀兼容"},
		{"new-topic", "", false, "- 对话：NewTag:new-topic", "新 Tag ⇒ NewTag:"},
		{"EndTag:cup-quota-0917", "", false, "- 对话：EndTag:cup-quota-0917", "新语法·结束"},
		{"End: Tag.cup-quota-0917", "", false, "- 对话：EndTag:cup-quota-0917", "旧写法·结束 ⇒ EndTag:"},
		{"new-topic", "", true, "- Conversation: NewTag:new-topic", "英文元数据行同样用统一语法"},
	}
	for _, c := range cases {
		got, errMsg, _ := chatSessionLine("ra_agent", b, c.session, c.reply, c.en)
		if errMsg != "" {
			t.Errorf("%s：session=%q 被拒：%s", c.why, c.session, errMsg)
			continue
		}
		if got != c.want {
			t.Errorf("%s：session=%q ⇒ %q，期望 %q", c.why, c.session, got, c.want)
		}
	}
}

// 统一语法不得放宽既有校验（End 权限 / 已结束 / 语法 / Re 目标存在）。
func TestChatSessionLineStillValidates(t *testing.T) {
	b := testBlocks()
	bad := []struct{ session, reply, why string }{
		{"cup quota", "", "含空格的短名应被拒"},
		{"", "144", "只给回应编号而不给 Tag 应被拒"},
		{"cup-quota-0917", "999", "回应不存在的编号应被拒"},
		{"EndTag:never-seen", "", "End 一个从未出现的 Tag 应被拒"},
		{"EndTag:old-topic", "", "重复 End 应被拒"},
	}
	for _, c := range bad {
		if _, errMsg, _ := chatSessionLine("ra_agent", b, c.session, c.reply, false); errMsg == "" {
			t.Errorf("%s（session=%q）", c.why, c.session)
		}
	}
	if _, errMsg, _ := chatSessionLine("棒Buddy", b, "EndTag:cup-quota-0917", "", false); errMsg == "" {
		t.Errorf("非发起人 EndTag 应被拒（发起人 = 该 Tag 最早一条的发言人）")
	}
	// 短名指向「已结束」的 Tag（非 End）同样应被拒 —— 归一的 Tag 必须参与 End 状态检查
	if _, errMsg, _ := chatSessionLine("棒Buddy", b, "old-topic", "", false); errMsg == "" {
		t.Errorf("向已结束的 Tag 发言应被拒")
	}
}
