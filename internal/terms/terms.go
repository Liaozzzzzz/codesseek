// Package terms maps natural-language intents to the identifier vocabulary a
// codebase is likely to use. Chinese queries are expanded with English code
// terms, and English identifiers are augmented with Chinese concepts so the
// Chinese embedding model can match them.
package terms

import (
	"strings"
	"unicode"
)

// maxAdded bounds how many extra terms one query may gain. The original query
// is always kept first; expansion only nudges retrieval toward code identifiers.
const maxAdded = 32

// entry is a set of Chinese concepts and their usual English code spellings.
// The table is deliberately about programming vocabulary, not one product:
// it maps natural-language intents to the identifiers a repository is likely
// to use, so a Chinese query can reach English code.
type entry struct {
	zh []string
	en []string
}

var glossary = []entry{
	{zh: []string{"登录", "登陆"}, en: []string{"login", "signin"}},
	{zh: []string{"退出", "登出"}, en: []string{"logout", "signout"}},
	{zh: []string{"注册"}, en: []string{"register", "signup"}},
	{zh: []string{"用户"}, en: []string{"user"}},
	{zh: []string{"账号", "账户"}, en: []string{"account"}},
	{zh: []string{"密码"}, en: []string{"password", "passwd"}},
	{zh: []string{"未授权", "登录失效", "登录过期", "登录态失效"}, en: []string{"unauthorized", "401"}},
	{zh: []string{"禁止访问", "无权限"}, en: []string{"forbidden", "403"}},
	{zh: []string{"未找到"}, en: []string{"notfound", "404"}},
	{zh: []string{"服务器错误"}, en: []string{"servererror", "500"}},
	{zh: []string{"请求超时"}, en: []string{"timeout", "timedout"}},
	{zh: []string{"验证码"}, en: []string{"captcha", "smscode", "verifycode"}},
	{zh: []string{"手机号", "手机"}, en: []string{"mobile", "phone"}},
	{zh: []string{"邮箱"}, en: []string{"email", "mail"}},
	{zh: []string{"短信"}, en: []string{"sms"}},
	{zh: []string{"令牌"}, en: []string{"token"}},
	{zh: []string{"会话"}, en: []string{"session"}},
	{zh: []string{"认证", "鉴权", "授权"}, en: []string{"auth", "authorize", "authentication"}},
	{zh: []string{"权限"}, en: []string{"permission", "auth", "authority"}},
	{zh: []string{"角色"}, en: []string{"role"}},
	{zh: []string{"菜单"}, en: []string{"menu"}},
	{zh: []string{"路由"}, en: []string{"router", "route"}},
	{zh: []string{"页面"}, en: []string{"page", "view"}},
	{zh: []string{"组件"}, en: []string{"component"}},
	{zh: []string{"弹窗", "对话框"}, en: []string{"modal", "dialog"}},
	{zh: []string{"表单"}, en: []string{"form"}},
	{zh: []string{"表格"}, en: []string{"table"}},
	{zh: []string{"分页"}, en: []string{"pagination", "pager"}},
	{zh: []string{"列表"}, en: []string{"list"}},
	{zh: []string{"详情"}, en: []string{"detail"}},
	{zh: []string{"搜索", "检索"}, en: []string{"search"}},
	{zh: []string{"查询"}, en: []string{"query"}},
	{zh: []string{"筛选", "过滤"}, en: []string{"filter"}},
	{zh: []string{"排序"}, en: []string{"sort"}},
	{zh: []string{"校验", "验证"}, en: []string{"validate", "verify", "check"}},
	{zh: []string{"提交"}, en: []string{"submit"}},
	{zh: []string{"保存"}, en: []string{"save"}},
	{zh: []string{"新增", "创建", "添加"}, en: []string{"create", "add"}},
	{zh: []string{"编辑", "修改"}, en: []string{"edit", "update", "modify"}},
	{zh: []string{"删除"}, en: []string{"delete", "remove"}},
	{zh: []string{"上传"}, en: []string{"upload"}},
	{zh: []string{"下载"}, en: []string{"download"}},
	{zh: []string{"导入"}, en: []string{"import"}},
	{zh: []string{"导出"}, en: []string{"export"}},
	{zh: []string{"预览"}, en: []string{"preview"}},
	{zh: []string{"打印"}, en: []string{"print"}},
	{zh: []string{"配置"}, en: []string{"config", "configuration"}},
	{zh: []string{"设置"}, en: []string{"setting", "settings"}},
	{zh: []string{"初始化"}, en: []string{"init", "initialize"}},
	{zh: []string{"缓存"}, en: []string{"cache"}},
	{zh: []string{"日志"}, en: []string{"log"}},
	{zh: []string{"错误"}, en: []string{"error"}},
	{zh: []string{"异常"}, en: []string{"exception", "error"}},
	{zh: []string{"失败"}, en: []string{"fail", "failure", "error"}},
	{zh: []string{"成功"}, en: []string{"success"}},
	{zh: []string{"提示"}, en: []string{"tip", "message"}},
	{zh: []string{"警告"}, en: []string{"warning", "warn"}},
	{zh: []string{"通知"}, en: []string{"notify", "notification"}},
	{zh: []string{"消息"}, en: []string{"message"}},
	{zh: []string{"请求"}, en: []string{"request"}},
	{zh: []string{"响应"}, en: []string{"response"}},
	{zh: []string{"接口"}, en: []string{"api", "interface"}},
	{zh: []string{"服务"}, en: []string{"service"}},
	{zh: []string{"状态"}, en: []string{"status", "state"}},
	{zh: []string{"数据"}, en: []string{"data"}},
	{zh: []string{"统计"}, en: []string{"statistics", "stat"}},
	{zh: []string{"报表"}, en: []string{"report"}},
	{zh: []string{"图表"}, en: []string{"chart"}},
	{zh: []string{"文件"}, en: []string{"file"}},
	{zh: []string{"图片"}, en: []string{"image", "picture"}},
	{zh: []string{"视频"}, en: []string{"video"}},
	{zh: []string{"音频"}, en: []string{"audio"}},
	{zh: []string{"附件"}, en: []string{"attachment"}},
	{zh: []string{"地址"}, en: []string{"address", "url"}},
	{zh: []string{"位置", "定位"}, en: []string{"location", "position"}},
	{zh: []string{"名称"}, en: []string{"name"}},
	{zh: []string{"标题"}, en: []string{"title"}},
	{zh: []string{"内容"}, en: []string{"content"}},
	{zh: []string{"描述", "说明"}, en: []string{"description", "desc"}},
	{zh: []string{"备注"}, en: []string{"remark", "comment"}},
	{zh: []string{"类型"}, en: []string{"type"}},
	{zh: []string{"分类"}, en: []string{"category"}},
	{zh: []string{"标签"}, en: []string{"tag", "label"}},
	{zh: []string{"编号"}, en: []string{"id", "serial"}},
	{zh: []string{"数量"}, en: []string{"count", "quantity"}},
	{zh: []string{"金额"}, en: []string{"amount"}},
	{zh: []string{"价格"}, en: []string{"price"}},
	{zh: []string{"时间"}, en: []string{"time"}},
	{zh: []string{"日期"}, en: []string{"date"}},
	{zh: []string{"订单"}, en: []string{"order"}},
	{zh: []string{"支付", "付款"}, en: []string{"pay", "payment"}},
	{zh: []string{"退款"}, en: []string{"refund"}},
	{zh: []string{"购物车"}, en: []string{"cart"}},
	{zh: []string{"商品", "产品"}, en: []string{"product", "goods"}},
	{zh: []string{"优惠券"}, en: []string{"coupon"}},
	{zh: []string{"会员"}, en: []string{"member"}},
	{zh: []string{"积分"}, en: []string{"points"}},
	{zh: []string{"收藏"}, en: []string{"favorite", "collect"}},
	{zh: []string{"评论"}, en: []string{"comment"}},
	{zh: []string{"点赞"}, en: []string{"like"}},
	{zh: []string{"分享"}, en: []string{"share"}},
	{zh: []string{"推荐"}, en: []string{"recommend"}},
	{zh: []string{"公告"}, en: []string{"notice"}},
	{zh: []string{"文章"}, en: []string{"article"}},
	{zh: []string{"文档"}, en: []string{"document", "doc"}},
	{zh: []string{"首页"}, en: []string{"home"}},
	{zh: []string{"头像"}, en: []string{"avatar"}},
	{zh: []string{"个人", "资料"}, en: []string{"profile"}},
	{zh: []string{"系统"}, en: []string{"system"}},
	{zh: []string{"管理", "后台"}, en: []string{"admin", "manage"}},
	{zh: []string{"处理"}, en: []string{"handle", "process"}},
	{zh: []string{"加载"}, en: []string{"load", "loading"}},
	{zh: []string{"刷新"}, en: []string{"refresh", "reload"}},
	{zh: []string{"返回"}, en: []string{"return", "back"}},
	{zh: []string{"确认"}, en: []string{"confirm"}},
	{zh: []string{"取消"}, en: []string{"cancel"}},
	{zh: []string{"完成"}, en: []string{"complete", "finish"}},
	{zh: []string{"封装"}, en: []string{"wrapper", "encapsulate"}},
	{zh: []string{"控制"}, en: []string{"control", "guard"}},
	{zh: []string{"拦截"}, en: []string{"intercept", "interceptor"}},
	{zh: []string{"跳转", "重定向"}, en: []string{"redirect", "navigate"}},
	{zh: []string{"窗口"}, en: []string{"window"}},
	{zh: []string{"浏览器"}, en: []string{"browser"}},
	{zh: []string{"存储"}, en: []string{"storage", "store"}},
	{zh: []string{"本地"}, en: []string{"local"}},
	{zh: []string{"远程"}, en: []string{"remote"}},
	{zh: []string{"环境"}, en: []string{"env", "environment"}},
	{zh: []string{"部署"}, en: []string{"deploy"}},
	{zh: []string{"构建"}, en: []string{"build"}},
	{zh: []string{"依赖"}, en: []string{"dependency", "depend"}},
	{zh: []string{"模块"}, en: []string{"module"}},
	{zh: []string{"工具"}, en: []string{"util", "utility", "tool"}},
	{zh: []string{"公共", "通用"}, en: []string{"common", "shared"}},
	{zh: []string{"常量"}, en: []string{"constant", "const"}},
	{zh: []string{"变量"}, en: []string{"variable", "var"}},
	{zh: []string{"函数"}, en: []string{"function", "func"}},
	{zh: []string{"方法"}, en: []string{"method"}},
	{zh: []string{"数组"}, en: []string{"array"}},
	{zh: []string{"字符串"}, en: []string{"string"}},
	{zh: []string{"转换"}, en: []string{"convert", "transform"}},
	{zh: []string{"解析"}, en: []string{"parse"}},
	{zh: []string{"格式化"}, en: []string{"format"}},
	{zh: []string{"编码"}, en: []string{"encode", "encoding"}},
	{zh: []string{"解码"}, en: []string{"decode", "decoding"}},
	{zh: []string{"加密"}, en: []string{"encrypt"}},
	{zh: []string{"解密"}, en: []string{"decrypt"}},
	{zh: []string{"签名"}, en: []string{"sign", "signature"}},
	{zh: []string{"时间戳"}, en: []string{"timestamp"}},
	{zh: []string{"定时"}, en: []string{"schedule", "timer"}},
	{zh: []string{"任务"}, en: []string{"task", "job"}},
	{zh: []string{"流程", "工作流"}, en: []string{"flow", "workflow"}},
	{zh: []string{"审批"}, en: []string{"approve", "approval"}},
	{zh: []string{"部门"}, en: []string{"department", "dept"}},
	{zh: []string{"组织"}, en: []string{"organization", "org"}},
	{zh: []string{"公司"}, en: []string{"company"}},
	{zh: []string{"员工"}, en: []string{"employee", "staff"}},
	{zh: []string{"客户"}, en: []string{"customer", "client"}},
	{zh: []string{"库存"}, en: []string{"stock", "inventory"}},
	{zh: []string{"物流"}, en: []string{"logistics"}},
	{zh: []string{"电话"}, en: []string{"phone", "tel"}},
	{zh: []string{"城市"}, en: []string{"city"}},
	{zh: []string{"省份", "地区"}, en: []string{"province", "region"}},
	{zh: []string{"二维码", "扫码"}, en: []string{"qrcode", "scan"}},
	{zh: []string{"主题"}, en: []string{"theme"}},
	{zh: []string{"样式"}, en: []string{"style"}},
	{zh: []string{"布局"}, en: []string{"layout"}},
	{zh: []string{"图标"}, en: []string{"icon"}},
	{zh: []string{"字体"}, en: []string{"font"}},
	{zh: []string{"颜色"}, en: []string{"color"}},
	{zh: []string{"动画"}, en: []string{"animation", "animate"}},
	{zh: []string{"适配"}, en: []string{"adapt", "responsive"}},
	{zh: []string{"移动端"}, en: []string{"mobile"}},
}

// maxDocKeywords bounds the Chinese hints appended to one chunk; the glossary
// is broad, and a short hint line should not drown out the code itself.
const maxDocKeywords = 20

// ExpandQuery appends English code terms for Chinese concepts in q, and Chinese
// terms for English identifiers. The original query stays intact and first.
func ExpandQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return q
	}
	lower := strings.ToLower(q)
	wordSet := map[string]bool{}
	for _, w := range words(q) {
		wordSet[w] = true
	}
	seen := map[string]bool{}
	for _, w := range words(q) {
		seen[w] = true
	}
	// Multi-character Chinese terms already in the query need no repeat.
	for _, e := range glossary {
		for _, z := range e.zh {
			if strings.Contains(lower, strings.ToLower(z)) {
				seen[strings.ToLower(z)] = true
			}
		}
	}

	var added []string
	add := func(term string) bool {
		key := strings.ToLower(term)
		if key == "" || seen[key] || len(added) >= maxAdded {
			return false
		}
		seen[key] = true
		added = append(added, term)
		return true
	}

	for _, e := range glossary {
		zhHit := false
		for _, z := range e.zh {
			if strings.Contains(lower, strings.ToLower(z)) {
				zhHit = true
				break
			}
		}
		enHit := false
		for _, en := range e.en {
			if wordSet[strings.ToLower(en)] {
				enHit = true
				break
			}
		}
		if !zhHit && !enHit {
			continue
		}
		for _, en := range e.en {
			add(en)
		}
		for _, z := range e.zh {
			add(z)
		}
		if len(added) >= maxAdded {
			break
		}
	}
	if len(added) == 0 {
		return q
	}
	return q + " " + strings.Join(added, " ")
}

// DocKeywords returns Chinese concepts implied by the English identifiers in
// text, for appending to a chunk's embedding input. Terms already present in
// the text are skipped, and the result is capped.
func DocKeywords(text string) []string {
	lower := strings.ToLower(text)
	wordSet := map[string]bool{}
	for _, w := range words(text) {
		wordSet[w] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range glossary {
		hit := false
		for _, en := range e.en {
			if identifierHasWord(wordSet, strings.ToLower(en)) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		for _, z := range e.zh {
			if seen[z] || strings.Contains(lower, z) {
				continue
			}
			seen[z] = true
			out = append(out, z)
			if len(out) >= maxDocKeywords {
				return out
			}
		}
	}
	return out
}

// identifierHasWord reports whether an identifier word set contains term.
// Identifiers often glue words together (getTokenbyCode), so longer terms may
// match as substrings; short terms stay exact to avoid noise.
func identifierHasWord(wordSet map[string]bool, term string) bool {
	if wordSet[term] {
		return true
	}
	if len(term) < 5 {
		return false
	}
	for w := range wordSet {
		if strings.Contains(w, term) {
			return true
		}
	}
	return false
}

// words lowercases and splits camelCase / snake_case / kebab-case text.
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			if len(cur) > 0 && !unicode.IsUpper(cur[len(cur)-1]) {
				flush()
			}
			cur = append(cur, r)
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}
