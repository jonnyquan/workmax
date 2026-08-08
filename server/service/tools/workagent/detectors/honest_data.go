package detectors

// HonestData detects fabricated metrics in the agent's output. This
// is the M6 P0 honest-placeholder enforcement.
//
// The agent should write `—` or "[转化率：待补充]" when it doesn't
// have a real number; this detector flags any output containing
// metrics that look fabricated by pattern. The decision to BLOCK
// vs WARN happens in the gate (PR-9): for ppt / marketingPoster
// flow this is P0 (block); for character / productShot it's P1
// (warn) since image-only output rarely contains text metrics.
//
// Patterns are language-aware. The Server's 18-locale catalog supports a
// global user base; per-locale fabricated-metric
// patterns avoid false positives ("3 个" in Chinese could be a
// legitimate count, but "提升 30%" without source is not).
//
// What we DON'T do: treat every percentage as fabricated. A user-
// provided number that the agent quotes back is fine. The honest-
// data check is a heuristic for "looks fabricated" — combined with
// the PROMPT-LEVEL injection of the protocol (DS-4 pre-flight),
// this catches both the "agent invented X" and "agent forgot to
// mark — when no source" failure modes.
//
// Implementation: thin wrapper over RegexScanner with a curated
// pattern set per category. Adding a language = add patterns to the
// existing categories.
var (
	honestDataInstance = NewRegexScanner("honest_data", map[string][]string{
		// "Conversion-rate / efficacy" claims without source.
		// English: "increased 30%", "boosted by 25%", "10× faster".
		// Chinese: "提升 30%", "节省 40%", "10 倍".
		"unsourced_efficacy_claim": {
			`\d+\s*%\s+(?i)(increase|improve|boost|reduce|cut|save)`,
			`(?i)(?:increased|improved|boosted|reduced|cut|saved)\s+(?:by\s+)?\d+\s*%`,
			`(?i)\d+\s*(?:×|x|times)\s+(?:faster|better|more|higher)`,
			`(?i)\bROI\s+(?:of\s+)?\d+(?:\s*[×x])?`, // "ROI of 5×" / "ROI 300%"
			`(?:提升|改善|提高|增加|增长|节省|缩短|减少|降低|节约)\s*\d+\s*%`,
			`\d+\s*倍\s*(?:更|的)?(?:快|慢|多|少|高|低|强|好)`,
			`ROI\s*\d+\s*[×倍]`,
		},

		// Round-number user / customer / revenue claims without
		// source. "10K users", "1M customers", "$500K saved".
		// Chinese: "10 万用户", "100 万客户", "节省 50 万".
		"unsourced_quantity_claim": {
			`\$\s*\d+\s*[KMB](?:\s+(?:saved|earned|gained))?`,
			`\d+\s*(?:K|M|B)\+\s+(?i)(users|customers|clients|teams|companies|installs|downloads)`,
			`(?i)trusted\s+by\s+\d+(?:[,\d]+)?\+?\s+(?:users|customers|teams|companies)`,
			`(?i)serves?\s+\d+(?:[,\d]+)?\+?\s+(?:users|customers)`,
			`\d+\s*万\s*(?:用户|客户|学员|学生|企业|团队|下载|安装)`,
			`(?:节省|赚取|创造|带来)\s*\d+\s*万`,
			`累计\s*(?:服务|帮助)?\s*\d+\s*万`,
		},

		// Time-to-value claims. "in 3 minutes", "set up in 5 seconds",
		// "launch in 30 days". Chinese: "3 分钟搞定", "30 天上线".
		"unsourced_speed_claim": {
			`(?i)in\s+(?:just|only)?\s*\d+\s+(?:minute|second|hour|day)s?`,
			`(?i)(?:set\s+up|launch|deploy|onboard)\s+in\s+\d+\s+(?:minute|second|hour|day)s?`,
			`(?:仅需|只需|只要|不到)\s*\d+\s*(?:分钟|秒|小时|天)\s*(?:即可|搞定|上线)`,
			`\d+\s*(?:分钟|秒|小时|天)\s*(?:搞定|完成|上线|交付|学会|掌握)`,
		},

		// Satisfaction / retention / rank claims. "90% satisfaction",
		// "industry-leading", "#1 in market".
		"unsourced_rank_or_share_claim": {
			`\d+\s*%\s+(?i)(?:satisfaction|retention|approval|positive|happy|accuracy)`,
			`(?i)(?:industry|market|category)[\s-]+(?:leading|leader|#1|number one)`,
			`(?i)(?:#1|no\.?\s*1|number one)\s+(?:in|of)`,
			`(?i)NPS\s+(?:of\s+)?\d+`,
			`\d+\s*%\s*(?:满意|好评|留存|续费|准确率|正确率)`,
			`(?:行业|市场|品类)\s*(?:领先|第一|领导|龙头|龙头老大)`,
			`(?:遥遥领先|断层第一|第一名)`,
		},
	})

	// HonestData is the package-level instance. Registered into
	// the default registry by init().
	HonestData = honestDataInstance
)

func init() {
	Default().Register(HonestData)
}

// Note: the var declaration above wraps RegexScanner's logic, but we
// also want a typed nil-safe Run for callers that hold a *HonestData
// field. The cleanest path is to expose HonestData as a *RegexScanner
// directly (since the underlying machinery IS a regex scanner) — no
// wrapper struct needed. Detector-specific behavior (locale-conditional
// patterns, etc.) can be added later by replacing the package-level
// instance with a struct that embeds *RegexScanner.

var _ Detector = (*RegexScanner)(nil)
