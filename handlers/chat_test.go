package handlers

import "testing"

// 2026-09-17：`Tag.` 前缀在**输入侧可省略**（沟通室 Web 端的 Tag 输入框只填短名，如 cup-quota-0917）。
// chatSessionLine 负责补全 / 规范化前缀，保证写入 CHAT.md 的始终是规范形式 `Tag.<短名>`。
func TestChatSessionLineTagPrefixOptional(t *testing.T) {
	blocks := []chatBlock{
		{No: "No.10", Tag: "Tag.cup-quota-0917", Speaker: "ra_agent"},
		{No: "No.11", Tag: "Tag.cup-quota-0917", Speaker: "棒球龙虾"},
		{No: "No.12", Tag: "Tag.old-topic", End: true, Speaker: "yaki"},
	}
	cases := []struct{ in, want, why string }{
		{"cup-quota-0917", "- 对话：Tag.cup-quota-0917", "只填短名 → 自动补 Tag."},
		{"Tag.cup-quota-0917", "- 对话：Tag.cup-quota-0917", "已带前缀 → 原样"},
		{"tag.cup-quota-0917", "- 对话：Tag.cup-quota-0917", "前缀大小写归一"},
		{"  cup-quota-0917  ", "- 对话：Tag.cup-quota-0917", "去首尾空白"},
		{"End: cup-quota-0917", "- 对话：End: Tag.cup-quota-0917", "End + 短名（发起人本人）"},
		{"End: tag.cup-quota-0917", "- 对话：End: Tag.cup-quota-0917", "End + 大小写归一"},
	}
	for _, c := range cases {
		got, errMsg, _ := chatSessionLine("ra_agent", blocks, c.in, "", false)
		if errMsg != "" {
			t.Errorf("%s：session=%q 被拒：%s", c.why, c.in, errMsg)
			continue
		}
		if got != c.want {
			t.Errorf("%s：session=%q ⇒ %q，期望 %q", c.why, c.in, got, c.want)
		}
	}

	// 英文元数据行同样支持短名（en=true 时写 `- Conversation:`）
	if got, errMsg, _ := chatSessionLine("ra_agent", blocks, "cup-quota-0917", "", true); errMsg != "" {
		t.Errorf("英文行：短名被拒：%s", errMsg)
	} else if got != "- Conversation: Tag.cup-quota-0917" {
		t.Errorf("英文行：⇒ %q", got)
	}

	// 补全之后**其它校验一条都不能少**
	if _, errMsg, _ := chatSessionLine("ra_agent", blocks, "cup quota", "", false); errMsg == "" {
		t.Errorf("含空格的标签应被拒（补全后应不匹配 Tag.<短名> 语法）")
	}
	if _, errMsg, _ := chatSessionLine("ra_agent", blocks, "cup-quota-0917", "No.999", false); errMsg == "" {
		t.Errorf("回应不存在的编号应被拒")
	}
	if _, errMsg, _ := chatSessionLine("棒Buddy", blocks, "old-topic", "", false); errMsg == "" {
		t.Errorf("短名指向「已结束」的会话应被拒（补全必须发生在 End 状态检查之前）")
	}
	if _, errMsg, _ := chatSessionLine("棒Buddy", blocks, "End: old-topic", "", false); errMsg == "" {
		t.Errorf("End 一个已结束的会话应被拒，不应因短名补全而放行")
	}
}
