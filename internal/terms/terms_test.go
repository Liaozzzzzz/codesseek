package terms

import (
	"strings"
	"testing"
)

func TestExpandChineseQueryAddsEnglishTerms(t *testing.T) {
	got := ExpandQuery("用户登录失败的处理")
	for _, want := range []string{"login", "user", "fail", "handle"} {
		if !hasWord(got, want) {
			t.Fatalf("ExpandQuery(%q) = %q, missing %q", "用户登录失败的处理", got, want)
		}
	}
}

func TestExpandEnglishQueryAddsChineseTerms(t *testing.T) {
	got := ExpandQuery("login error")
	for _, want := range []string{"登录", "错误"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ExpandQuery(%q) = %q, missing %q", "login error", got, want)
		}
	}
}

func TestExpandCamelCaseQuery(t *testing.T) {
	got := ExpandQuery("handleLoginError")
	for _, want := range []string{"登录", "错误", "处理"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ExpandQuery(%q) = %q, missing %q", "handleLoginError", got, want)
		}
	}
}

func TestExpandLeavesUnknownQueryAlone(t *testing.T) {
	q := "zzzz qqqq"
	if got := ExpandQuery(q); got != q {
		t.Fatalf("ExpandQuery(%q) = %q", q, got)
	}
}

func TestExpandDoesNotRepeatWords(t *testing.T) {
	got := ExpandQuery("login 登录")
	words := strings.Fields(got)
	seen := map[string]bool{}
	for _, w := range words {
		if seen[w] {
			t.Fatalf("ExpandQuery produced duplicate word %q: %q", w, got)
		}
		seen[w] = true
	}
}

func TestExpandCapsAddedTerms(t *testing.T) {
	q := "用户 登录 注册 密码 权限 角色 菜单 路由 页面 组件 表单 列表 详情 搜索 校验 提交 保存 新增 编辑 删除 上传 下载 配置 日志 错误 异常 失败 请求 响应 接口 服务 状态 数据 文件 图片 订单 支付 商品 会员 收藏 评论"
	got := ExpandQuery(q)
	if n := len(strings.Fields(got)) - len(strings.Fields(q)); n > maxAdded {
		t.Fatalf("added %d terms, want <= %d", n, maxAdded)
	}
}

func TestExpandStatusCodeQuery(t *testing.T) {
	got := ExpandQuery("401 未授权处理")
	for _, want := range []string{"unauthorized", "登录失效", "handle"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ExpandQuery(401 未授权处理) = %q, missing %q", got, want)
		}
	}
}

func TestDocKeywordsFromStatusCode(t *testing.T) {
	got := strings.Join(DocKeywords("if (response.status === 401) { handleResponse() }"), " ")
	if !strings.Contains(got, "未授权") {
		t.Fatalf("DocKeywords = %q, missing 未授权", got)
	}
}

func TestDocKeywordsFromIdentifiers(t *testing.T) {
	got := DocKeywords("app/composables/useLogin.ts function useLogin getLoginUrl userApi getTokenbyCode")
	joined := strings.Join(got, " ")
	for _, want := range []string{"登录", "用户", "令牌", "接口"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("DocKeywords = %v, missing %q", got, want)
		}
	}
	if len(got) > maxDocKeywords {
		t.Fatalf("DocKeywords returned %d terms, want <= %d", len(got), maxDocKeywords)
	}
}

func TestDocKeywordsSkipsTextItAlreadyHas(t *testing.T) {
	got := DocKeywords("登录 login handler")
	for _, kw := range got {
		if kw == "登录" {
			t.Fatalf("DocKeywords repeated an existing term: %v", got)
		}
	}
}

func TestDocKeywordsIgnoresUnknownIdentifiers(t *testing.T) {
	if got := DocKeywords("zzzz qqqq"); len(got) != 0 {
		t.Fatalf("DocKeywords = %v, want none", got)
	}
}

func hasWord(s, word string) bool {
	for _, w := range words(s) {
		if w == word {
			return true
		}
	}
	return false
}
