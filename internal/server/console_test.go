package server

import (
	"strings"
	"testing"
)

// 控制台是内嵌单页，改动容易静默回归（例如前后端字段名不一致）。
// 这里守住几条关键约定：主题、分页、以及模型字段的 camelCase。
func TestConsoleHTMLContracts(t *testing.T) {
	html := string(consoleHTML)
	if html == "" {
		t.Fatal("consoleHTML 为空")
	}

	must := map[string]string{
		"主题按钮":    `id="theme"`,
		"浅色变量":    `:root[data-theme="light"]`,
		"分页容器":    `id="acctPager"`,
		"分页信息":    `id="acctInfo"`,
		"每页选择":    `id="acctSize"`,
		"上一页":     `id="acctPrev"`,
		"下一页":     `id="acctNext"`,
		"分页渲染":    `updateAcctPager`,
		"分页状态":    `acctPageSize`,
		"主题应用":    `applyTheme`,
		"主题持久化":   `deepaicode2api_theme`,
		"分页大小持久化": `deepaicode2api_acct_page_size`,
	}
	for name, needle := range must {
		if !strings.Contains(html, needle) {
			t.Errorf("控制台缺少%s：%s", name, needle)
		}
	}

	// 模型目录走 camelCase（deepai.Model 的 json tag），页面必须按 camelCase 取值。
	for _, field := range []string{"m.theosPerRequest", "m.contextWindow", "m.maxTokens", "m.input", "m.premier"} {
		if !strings.Contains(html, field) {
			t.Errorf("模型目录未按 json tag 取值：%s", field)
		}
	}
	for _, wrong := range []string{"m.TheosPerRequest", "m.ContextWindow", "m.MaxTokens", "m.Input"} {
		if strings.Contains(html, wrong) {
			t.Errorf("模型目录仍在使用 Go 字段名（会渲染成空）：%s", wrong)
		}
	}
}

// 分页默认 20，且只在 20/50/100 里取值。
func TestConsoleDefaultPageSize(t *testing.T) {
	html := string(consoleHTML)
	if !strings.Contains(html, "var acctPageSize = 20;") {
		t.Error("分页默认值应为 20")
	}
	// 只看 acctSize 这个 select 内部的选项（页面别处也有 value="200" 的控件）。
	start := strings.Index(html, `id="acctSize"`)
	if start < 0 {
		t.Fatal("找不到 acctSize 选择框")
	}
	end := strings.Index(html[start:], "</select>")
	if end < 0 {
		t.Fatal("acctSize 选择框未闭合")
	}
	block := html[start : start+end]
	for _, v := range []string{`value="20"`, `value="50"`, `value="100"`} {
		if !strings.Contains(block, v) {
			t.Errorf("分页选择框缺少选项 %s", v)
		}
	}
	if strings.Contains(block, `value="200"`) {
		t.Error("分页选择框不应有 200 选项")
	}
}
