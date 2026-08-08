// Package workagent — question_form_inferer.go.
//
// NLP pre-scan that lets the M1 short-circuit skip the form when the
// user's first message already implies enough context. Auto-skip is
// one of three "feature is on but the form
// won't fire" paths (the other two are URL-based fork and per-user
// settings).
//
// The inferer is intentionally simple: regex / keyword match against
// the first user message, populated with hand-curated dictionaries
// per skill + question. Coverage is partial by design — we only
// trip auto-skip when ALL required questions get a confident match.
// A single match isn't enough to skip; the user still gets the form
// with that field pre-filled.
//
// What this is NOT:
//
//   - Not a parser of structured natural language. "20-page PPT for
//     execs" gets matched by the patterns; "give me a deck" gets
//     nothing and we show the form.
//   - Not LLM-based. We could ask the model "did the user specify
//     audience / tone / scale?" but that's a token cost on every
//     turn-1, and an LLM-resolved answer is no better than the form
//     it's trying to skip.
//   - Not a future replacement for the form. The form is the
//     contract; this is a 60% optimization.
package workagent

import (
	"regexp"
	"strings"

	"server/service/tools/workagent/skills"
)

// InferenceResult is the inferer's verdict for one user message.
type InferenceResult struct {
	// AllRequiredFilled is true when every required question has a
	// confident match. The dispatcher reads this to decide auto-skip.
	AllRequiredFilled bool

	// Answers holds the inferred field values. Stable string values
	// matching the skill's question.Options[].Value.
	Answers map[string]string

	// Trace lists which patterns matched per question, for debugging
	// when an expected match doesn't fire.
	Trace map[string][]string
}

// InferFormAnswers scans the user message against the skill's
// question_form schema. Returns a partial result when only some
// questions match — caller decides whether to auto-skip, prefill the
// emitted form, or ignore the result.
//
// The matching is per-question, per-language. We tolerate mixed
// English / Chinese in the same message (a real user pattern,
// especially in workmax's market) by running all locales' patterns.
// First confident match per question wins.
func InferFormAnswers(userMessage string, qf *skills.QuestionForm) InferenceResult {
	result := InferenceResult{
		Answers: map[string]string{},
		Trace:   map[string][]string{},
	}
	if qf == nil || userMessage == "" {
		return result
	}

	normalized := strings.ToLower(userMessage)

	requiredCount := 0
	matchedRequired := 0

	for _, question := range qf.Questions {
		if question.Required {
			requiredCount++
		}

		val, hits := inferOneQuestion(normalized, question)
		if val != "" {
			result.Answers[question.ID] = val
			result.Trace[question.ID] = hits
			if question.Required {
				matchedRequired++
			}
		}
	}

	// All-required-filled = auto-skip eligible. Zero required
	// questions degenerates to "always skip" — but that's a
	// pathological skill config (form with no required fields
	// shouldn't be configured at all).
	result.AllRequiredFilled = requiredCount > 0 && matchedRequired == requiredCount
	return result
}

// inferOneQuestion runs the curated patterns for question.ID against
// the message and returns the first matching option.Value (empty
// string when nothing matches). The Trace entry collects every
// matched pattern raw text so debug output can show why a
// particular value won.
//
// Pattern selection: hard-coded by question ID. We could make this
// data-driven via the yaml manifest, but baking patterns in Go
// keeps the language-aware regex / dictionary work in one reviewable
// place; manifest authors don't need to learn regex.
func inferOneQuestion(normalized string, q skills.QuestionFormField) (string, []string) {
	patterns, ok := questionPatterns[q.ID]
	if !ok {
		return "", nil
	}

	// Iterate the question's options — first option whose
	// patterns match wins. Order matters: more specific options
	// should be listed first in the patterns map (e.g. "investor"
	// before "exec" since both contain "exec" — wait, no, they
	// don't, but you get the principle).
	for _, opt := range q.Options {
		ps, ok := patterns[opt.Value]
		if !ok {
			continue
		}
		for _, re := range ps {
			if matched := re.FindString(normalized); matched != "" {
				return opt.Value, []string{matched}
			}
		}
	}
	return "", nil
}

// mustCompile is a panic-on-malformed wrapper that runs at package
// init time. Same rationale as the regex_scanner detector — a bad
// pattern is a build-time bug, not a runtime one.
func mustCompile(raw string) *regexp.Regexp {
	return regexp.MustCompile(raw)
}

// questionPatterns is the curated dictionary keyed by (question ID,
// option value). All regexes match against lowercased input.
//
// Coverage philosophy: aim for 60% recall on the most common
// phrasings. False positives are worse than false negatives — a
// false positive auto-skips with the wrong answer, which is more
// frustrating than seeing an extra form. We err toward narrow
// patterns.
//
// Sources for the dictionary entries: hand-curated from typical
// product / industry vocabulary in zh + en. The thresholds are
// based on PR-6's spec ("≥ 30 phrases per mode").
var questionPatterns = map[string]map[string][]*regexp.Regexp{

	// ============================================================
	// PPT mode questions
	// ============================================================
	"audience": {
		"investor": []*regexp.Regexp{
			mustCompile(`(?i)\binvestors?\b|\bvc\b|\bseries\s+[a-z]\b|\bpitch\s+decks?\b`),
			mustCompile(`投资人|路演|风投|融资`),
		},
		"exec": []*regexp.Regexp{
			mustCompile(`(?i)\bexecutives?\b|\bexec\b|\bc[-\s]?suite\b|\bboard\s+meetings?\b|\bquarterly\s+board\b`),
			mustCompile(`高管|管理层|董事会|总裁|总经理`),
		},
		"internal": []*regexp.Regexp{
			mustCompile(`(?i)\binternal\s+teams?\b|\bteam\s+(?:meetings?|updates?)\b|\ball[\s-]hands\b`),
			mustCompile(`内部团队|团队会|内部分享|全员`),
		},
		"public": []*regexp.Regexp{
			mustCompile(`(?i)\bmarketing\b|\bpublic\s+(?:audience|launch)\b|\bcustomer\s+facing\b`),
			mustCompile(`营销|对外发布|公众|客户面向`),
		},
		"professional": []*regexp.Regexp{
			mustCompile(`(?i)\bprofessional[s]?\b|\bb2b\b|\benterprise\b`),
			mustCompile(`职场|专业人士|企业级`),
		},
		"gen_z": []*regexp.Regexp{
			mustCompile(`(?i)\bgen\s*z\b|\bgenz\b|\byouth\s+market\b|\b18[-\s]?24\b`),
			mustCompile(`z\s*世代|青年|95后|00后`),
		},
		"millennial": []*regexp.Regexp{
			mustCompile(`(?i)\bmillennial[s]?\b|\b25[-\s]?34\b`),
			mustCompile(`千禧|85后|90后`),
		},
		"family": []*regexp.Regexp{
			mustCompile(`(?i)\bfamily\b|\bparents?\b|\bkids?\b`),
			mustCompile(`家庭|亲子|父母|宝宝`),
		},
	},

	"tone": {
		"editorial_magazine": []*regexp.Regexp{
			mustCompile(`(?i)\beditorial\b|\bmagazine\s+style\b|\bnyt\b`),
			mustCompile(`杂志|编辑感|报刊`),
		},
		"modern_minimal": []*regexp.Regexp{
			mustCompile(`(?i)\bminimal(?:ist)?\b|\bclean\b|\bsimple\b|\blinear\b|\bvercel\b`),
			mustCompile(`极简|现代|简约|干净`),
		},
		"bold_editorial": []*regexp.Regexp{
			mustCompile(`(?i)\bbold\b|\bdramatic\b|\bdaring\b|\bhigh[-\s]contrast\b`),
			mustCompile(`大胆|强烈|高对比|抢眼`),
		},
		"tech_utility": []*regexp.Regexp{
			mustCompile(`(?i)\bdashboard\b|\btechnical\b|\bdata[-\s]heavy\b|\bbloomberg\b`),
			mustCompile(`数据|技术|工具感|信息密度|仪表盘`),
		},
	},

	"scale": {
		"short": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:5|6|7|8|9|10)\s*[-\s]?(?:pages?|slides?|张|页)`),
			mustCompile(`(?:5|6|7|8|9|10)\s*[页张]`),
			mustCompile(`(?i)\b(?:short|brief|quick)\s+(?:decks?|presentations?)\b`),
			mustCompile(`简短|精简版`),
		},
		"medium": []*regexp.Regexp{
			mustCompile(`(?i)\b1[1-9]\s*[-\s]?(?:pages?|slides?|张|页)`),
			mustCompile(`1[1-9]\s*[页张]`),
		},
		"long": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:2[0-9]|[3-9][0-9]|\d{3,})\s*[-\s]?(?:pages?|slides?|张|页)`),
			mustCompile(`(?:2[0-9]|[3-9][0-9]|\d{3,})\s*[页张]`),
			mustCompile(`(?i)\bdetailed\s+(?:decks?|presentations?)\b|\bcomprehensive\b`),
			mustCompile(`详细|全面|长篇`),
		},
	},

	// ============================================================
	// character mode
	// ============================================================
	"role": {
		"protagonist": []*regexp.Regexp{
			mustCompile(`(?i)\bprotagonist\b|\bmain\s+character\b|\bhero\b`),
			mustCompile(`主角|主人公|男主|女主`),
		},
		"supporting": []*regexp.Regexp{
			mustCompile(`(?i)\bsupporting\s+(?:role|character)\b|\bsidekick\b`),
			mustCompile(`配角|次要角色`),
		},
		"brand_mascot": []*regexp.Regexp{
			mustCompile(`(?i)\bmascot\b|\bbrand\s+character\b`),
			mustCompile(`吉祥物|品牌形象`),
		},
	},

	"outfit": {
		"business": []*regexp.Regexp{
			mustCompile(`(?i)\bbusiness\s+(?:suit|attire|wear)\b|\bsuit\s+and\s+tie\b`),
			mustCompile(`商务装|西装`),
		},
		"formal": []*regexp.Regexp{
			mustCompile(`(?i)\bformal\b|\bevening\s+(?:gown|wear)\b|\btuxedo\b`),
			mustCompile(`正装|礼服|燕尾`),
		},
		"casual": []*regexp.Regexp{
			mustCompile(`(?i)\bcasual\b|\beveryday\b|\bjeans\b|\bt[-\s]shirt\b`),
			mustCompile(`休闲|日常|t恤|牛仔`),
		},
		"uniform": []*regexp.Regexp{
			mustCompile(`(?i)\buniform\b|\bworkwear\b`),
			mustCompile(`制服|工装`),
		},
	},

	"camera_angle": {
		"front": []*regexp.Regexp{
			mustCompile(`(?i)\bfront\s+(?:view|angle|shot)\b|\bstraight[-\s]on\b`),
			mustCompile(`正面|正视`),
		},
		"three_quarter": []*regexp.Regexp{
			mustCompile(`(?i)\bthree[-\s]quarter\b|\b3/4\s+view\b`),
			mustCompile(`四分之三|3/4`),
		},
		"profile": []*regexp.Regexp{
			mustCompile(`(?i)\bprofile\b|\bside\s+view\b`),
			mustCompile(`侧面|侧视`),
		},
		"full_body": []*regexp.Regexp{
			mustCompile(`(?i)\bfull[-\s]body\b|\bwhole\s+body\b`),
			mustCompile(`全身|全景`),
		},
	},

	// ============================================================
	// productShot mode
	// ============================================================
	"product_type": {
		"electronics": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:phone|laptop|tablet|earbud|headphone|watch)\b|\belectronic[s]?\b`),
			mustCompile(`手机|电脑|耳机|手表|平板|电子产品`),
		},
		"apparel": []*regexp.Regexp{
			mustCompile(`(?i)\bapparel\b|\bclothing\b|\bjacket\b|\bdress\b|\bshoes?\b`),
			mustCompile(`服装|衣服|外套|裙|鞋|服饰`),
		},
		"beauty": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:lipstick|skincare|cosmetic|makeup|perfume)\b`),
			mustCompile(`口红|护肤|化妆品|香水|彩妆`),
		},
		"food": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:food|beverage|drink|snack|coffee|tea)\b`),
			mustCompile(`食品|饮料|零食|咖啡|茶|食物`),
		},
		"home": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:furniture|home\s+goods|kitchen|appliance)\b`),
			mustCompile(`家具|家居|厨房|家电`),
		},
	},

	"setting": {
		"studio": []*regexp.Regexp{
			mustCompile(`(?i)\bstudio\b|\bwhite\s+background\b|\bclean\s+(?:bg|background)\b`),
			mustCompile(`影棚|纯色背景|白底`),
		},
		"lifestyle": []*regexp.Regexp{
			mustCompile(`(?i)\blifestyle\b|\bin[-\s]use\b|\bkitchen\s+scene\b`),
			mustCompile(`生活场景|生活方式|场景化`),
		},
		"outdoor": []*regexp.Regexp{
			mustCompile(`(?i)\boutdoor\b|\boutside\b|\bnature\b|\bpark\b`),
			mustCompile(`户外|室外|自然`),
		},
		"flat_lay": []*regexp.Regexp{
			mustCompile(`(?i)\bflat[-\s]lay\b|\btop[-\s]down\b|\boverhead\s+shot\b`),
			mustCompile(`平铺|俯拍`),
		},
	},

	"lighting": {
		"soft": []*regexp.Regexp{
			mustCompile(`(?i)\bsoft\s+(?:light|lighting)\b|\bdiffused\b`),
			mustCompile(`柔光|柔和光|散射光`),
		},
		"dramatic": []*regexp.Regexp{
			mustCompile(`(?i)\bdramatic\s+(?:light|lighting)\b|\bhard\s+light\b`),
			mustCompile(`戏剧光|强烈光|硬光`),
		},
		"natural": []*regexp.Regexp{
			mustCompile(`(?i)\bnatural\s+(?:light|lighting)\b|\bdaylight\b|\bsunlight\b`),
			mustCompile(`自然光|阳光|日光`),
		},
		"golden_hour": []*regexp.Regexp{
			mustCompile(`(?i)\bgolden\s+hour\b|\bsunset\b|\bsunrise\b`),
			mustCompile(`黄金时刻|日落|日出`),
		},
	},

	// ============================================================
	// marketingPoster mode (audience also reused above)
	// ============================================================
	"offer": {
		"discount": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:discount|sale|\d+%\s+off)\b|\bpromo\b`),
			mustCompile(`促销|折扣|打折|优惠`),
		},
		"launch": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:launch|release|new\s+product)\b`),
			mustCompile(`发布|新品|上市|首发`),
		},
		"event": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:event|conference|summit|webinar)\b`),
			mustCompile(`活动|大会|峰会|线上`),
		},
		"brand_awareness": []*regexp.Regexp{
			mustCompile(`(?i)\bbrand\s+awareness\b|\bawareness\s+campaign\b`),
			mustCompile(`品牌曝光|品宣|品牌建设`),
		},
	},

	"cta_style": {
		"urgent": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:limited\s+time|urgent|hurry|act\s+now)\b`),
			mustCompile(`限时|紧迫|抢|立刻`),
		},
		"aspirational": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:join\s+us|imagine|inspire|aspirational)\b`),
			mustCompile(`向往|憧憬|加入我们|愿景`),
		},
		"informative": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:learn\s+more|info|details|spec)\b`),
			mustCompile(`了解|详情|查看|信息`),
		},
	},

	// ============================================================
	// flashCard mode
	// ============================================================
	"subject": {
		"language": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:vocabulary|grammar|english|spanish|french)\b`),
			mustCompile(`英语|单词|语言|词汇|语法`),
		},
		"science": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:biology|chemistry|physics|science)\b`),
			mustCompile(`生物|化学|物理|科学`),
		},
		"math": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:math|algebra|geometry|calculus)\b`),
			mustCompile(`数学|代数|几何`),
		},
		"history": []*regexp.Regexp{
			mustCompile(`(?i)\bhistory\b|\bworld\s+war\b`),
			mustCompile(`历史|朝代`),
		},
		"general": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:trivia|general\s+knowledge|gk)\b`),
			mustCompile(`通识|常识|知识点`),
		},
	},

	"age_group": {
		"preschool": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:preschool|kindergarten|toddler|3[-\s]?5\s+years?)\b`),
			mustCompile(`学前|幼儿园|3-5岁`),
		},
		"elementary": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:elementary|primary|grade\s+[1-5]|6[-\s]?10\s+years?)\b`),
			mustCompile(`小学|6-10岁`),
		},
		"middle": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:middle\s+school|grade\s+[6-9]|11[-\s]?14\s+years?)\b`),
			mustCompile(`初中|中学|11-14岁`),
		},
		"adult": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:adult|college|grad)\s+(?:learner|student)\b`),
			mustCompile(`成人|成年|大学|研究生`),
		},
	},

	"count": {
		"small": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:5|10|few|small)\s+cards?\b`),
			mustCompile(`(?:5|10)\s*张?\s*卡`),
		},
		"medium": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:1[5-9]|2[0-5])\s+cards?\b`),
			mustCompile(`(?:1[5-9]|2[0-5])\s*张?\s*卡`),
		},
		"large": []*regexp.Regexp{
			mustCompile(`(?i)\b(?:3\d|[4-9]\d|\d{3,})\s+cards?\b`),
			mustCompile(`(?:3\d|[4-9]\d|\d{3,})\s*张?\s*卡`),
		},
	},
}
