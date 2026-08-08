# 核心资产协议(涉及具体品牌时强制执行)

## RULE 0:永不凭记忆猜品牌色 / 字体 / Logo

**这是 P0 硬规则,违反 = 直接重做。**

如果你写下任何 hex 色值(`#1A2B3C`)、rgb(`rgb(26,43,60)`)、字体名(`"Söhne"` `"Inter"`),**必须**能溯源到下列三类源之一:

1. 用户在本会话内上传的资产文件(logo SVG / brand guidelines PDF / 截图)
2. 你在本会话内 fetch 的官方 URL(brand-spec.md 已记录) 
3. 系统预置的 design-system markdown(active design-system 已锁定)

无溯源 → 改为占位符 `—` 或 `[品牌色:待提供]`,**绝不**写"看起来像 X 公司应该用的颜色"。

> 真实失败模式:模型常把"Anthropic 的红"猜成"Stripe 的紫蓝",或反过来。**凭记忆猜色 = 品牌灾难**。

## 触发条件

任务涉及**具体品牌**——用户提了产品名 / 公司名 / 明确客户(品牌名、产品名、自家公司),**不论用户是否主动提供了品牌资料**。

## 前置硬条件

走协议前必须已通过`fact-verification`确认品牌 / 产品**存在且状态已知**。如果你还不确定产品是否已发布 / 规格 / 版本,先回去搜。

## 核心理念:资产 > 规范

**品牌的本质是「它被认出来」**。认出来靠什么?按识别度排序:

| 资产类型 | 识别度贡献 | 必需性 |
|---|---|---|
| **Logo** | 最高 · 任何品牌出现 logo 就一眼识别 | **任何品牌都必须有** |
| **产品图 / 产品渲染图** | 极高 · 实体产品的"主角"就是产品本身 | **实体产品必须有** |
| **UI 截图 / 界面素材** | 极高 · 数字产品的"主角"是它的界面 | **数字产品必须有** |
| **色值** | 中 · 辅助识别,脱离前三项时经常撞衫 | 辅助 |
| **字体** | 低 · 需配合前述才能建立识别 | 辅助 |
| **气质关键词** | 低 · agent 自检用 | 辅助 |

## 翻译成执行规则

- 只抽色值 + 字体、不找 logo / 产品图 / UI → **违反本协议**
- 用 CSS 剪影 / SVG 手画替代真实产品图 → **违反本协议**(生成的就是「通用科技动画」,任何品牌都长一样)
- 找不到资产不告诉用户、也不 AI 生成,硬做 → **违反本协议**
- **宁可停下问用户要素材,也不要用 generic 填充**

## 5 步硬流程

### Step 1 · 问(资产清单一次问全)

不要只问「有 brand guidelines 吗?」——太宽泛,用户不知道该给什么。按清单逐项问:

```
关于 <brand/product>,你手上有以下哪些资料?我按优先级列:
1. Logo(SVG / 高清 PNG)—— 任何品牌必备
2. 产品图 / 官方渲染图 —— 实体产品必备
3. UI 截图 / 界面素材 —— 数字产品必备
4. 色值清单(HEX / RGB / 品牌色盘)
5. 字体清单(Display / Body)
6. Brand guidelines PDF / 品牌官网链接

有的直接发我,没有的我去搜 / 抓 / 生成。
```

### Step 2 · 搜官方渠道

| 资产 | 搜索路径 |
|---|---|
| Logo | `<brand>.com/brand` / `<brand>.com/press` / `brand.<brand>.com` / 官网 header inline SVG |
| 产品图 | 官网产品页 / 官方新闻稿 / Wikimedia / 维基百科条目 |
| UI 截图 | 官网截图 / App Store 截图 / 真实使用截图 |
| 色值 | brand guidelines / dribbble.com 同名搜索 |

### Step 3 · 验证

拿到的资产**必须人眼或工具校验**——logo 是否是最新版本?产品图是否清晰可用?UI 截图是否包含敏感数据?

### Step 4 · 落到产物

- Logo 必须用真实 SVG / PNG,不要用文字"Brand Logo"占位
- 产品图必须用真实图片,不要用 CSS 阴影 / SVG 形状代替
- UI 截图保留品牌真实视觉风格,不要二次设计

### Step 5 · 找不到资产时

**明确告诉用户**:"我搜了 X / Y / Z 没找到 logo,有以下选项:(a) 你提供;(b) 我用占位符标注「待替换」;(c) 我跳过这部分。"

**绝不**:静默用 generic 填充,假装做完了。

---

## Brand-Spec 提取工作流(M4 升级)

当用户**提供了品牌引用**(截图 / URL / 品牌名 / brand_assets 已存 row),走下面 5 步硬流程。这是 RULE 0 的具体执行手册——结束后会有一份 `./brand-spec.md` 落到 thread workdir,后续轮次直接读取这份文件,不重走流程。

### Step 1 · Locate(识别主源)

按优先级判定主源:

1. 用户上传的 logo 文件(SVG / 高清 PNG)—— 最权威
2. 用户提供的官网 URL —— 用 WebFetch 抓
3. brand_assets DB 已存 row(系统知道的品牌)
4. 仅有品牌名字符串 —— 弱,必须 Step 2 实抓

**禁止**直接进 Step 4 codify 不做实抓。

### Step 2 · Download(实际抓取到磁盘)

字节**必须**落盘到 `./.cache/brand/<slug>/`:

| 源类型 | 抓取手段 |
|---|---|
| 用户文件 | Read → Write to `.cache/brand/<slug>/<filename>` |
| URL | WebFetch → Write |
| DB row | SELECT 出 payload → Write JSON 副本 |
| 品牌名 | Web 搜官网 URL → 走 URL 路径 |

**没字节落盘 ⇒ 禁止 Step 3**。

### Step 3 · Grep Hex(从源抓真实色值)

| 源类型 | 提取方法 |
|---|---|
| 图片 | sample-color tool 取 5 个主色,按面积排序 |
| URL | 抓 CSS,grep `#[0-9a-fA-F]{6}` 与 `rgb\(\d+,\s*\d+,\s*\d+\)` |
| DB row | 读 `payload.colors[]` |
| PDF | OCR 后 grep |

**禁止写无源 hex**。Grep 不到 → 告诉用户"找到 logo 但提不出色值,请直接告诉我品牌主色 hex"。

### Step 4 · Codify(写 `brand-spec.md` 9-section schema)

落盘到 thread workdir 根目录的 `./brand-spec.md`,**必须**包含 9 段:

```markdown
# Brand Spec — <Brand Name>

## 1. Color
- Primary:   #RRGGBB  · 来源: <source>
- Secondary: ...
- Neutral:   ...
- Semantic:  success #... / warning #... / error #... / info #...

## 2. Typography
- Display: <font-stack>  · weight 700 · sizes [48, 36, 28]
- Body:    <font-stack>  · weight 400/500
- Mono:    <font-stack>

## 3. Spacing
- Scale: 4 / 8 / 12 / 16 / 24 / 32 / 48 / 64
- Grid:  12-col · gutter 24

## 4. Layout
- Containers: max-w-[1200px]
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280

## 5. Components
- Button (primary / secondary / ghost) — radius / padding / weight
- Card — radius / shadow / padding
- Form fields — input / textarea / select 基础样式

## 6. Motion
- Fast:    150ms ease-out
- Default: 250ms ease-in-out
- Slow:    400ms ease-in-out

## 7. Voice
- Tone keywords: <3-5 个>
- Do say:    [...]
- Don't say: [...]

## 8. Brand
- Logo usage / clear-space / minimum-size
- Don'ts: 不变形 / 不换色 / 不加阴影

## 9. Anti-patterns
- 什么样子算"不像这个品牌"(具体反例)
```

每个 hex / 字体 / 数值都要附 `· 来源: <抓取源>` 注释,后续 Step 5 + 验证 detector 可追溯。

### Step 5 · Vocalize(口头复述确认)

**生成任何 artifact 前**,把抓到的 spec 摘要写回给用户,要求确认:

```
基于 <source>,我提取到的品牌规范:
- 主色:#1A2B3C(来自 logo SVG)
- 辅色:#FFEEDD(来自官网 CSS)
- 字体:Söhne(display)/ Inter(body)
- 调性:minimal · technical · trustworthy

是否确认?或需要调整?(回复 "ok" 进入设计;或指出错处)
```

等用户:
- 显式确认 → 进 generation
- 显式调整 → 改 brand-spec.md 后再 Vocalize 一次
- 显式跳过 → 标注 brand-spec.md 为 `[unconfirmed]` 状态,后续产物加显眼"待品牌方确认"水印

**绝不**:Vocalize 之后没等用户回复就直接生成最终 artifact。
