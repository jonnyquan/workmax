# 反 AI Slop 黑名单

> AI 默认产出里最容易掉进去的陷阱。这是一份「不做什么」的清单,比「做什么」更重要——因为 AI slop 是默认值,你不主动避免就会发生。

## 视觉陷阱

### ❌ 激进渐变背景
- 紫色 → 粉色 → 蓝色全屏渐变(AI 生成网页的典型味道)
- 任何方向的 rainbow gradient
- Mesh gradient 铺满背景
- ✅ 如果要用渐变:subtle、单色系、有意图地点缀(比如 button hover)

### ❌ 圆角卡片 + 左 border accent 色

这种卡片在 AI 生成的 Dashboard 里泛滥:
```css
.card { border-radius: 12px; border-left: 4px solid #3b82f6; padding: 16px; }
```
想做强调?用更有设计感的方式:背景色对比、字重 / 字号对比、plain 分隔线、或者干脆不分卡片。

### ❌ Emoji 装饰

除非品牌本身使用 emoji(比如 Notion、Slack),否则**不要**在 UI 上放 emoji。**尤其不要**:
- 标题前的 🚀 ⚡️ ✨ 🎯 💡
- Feature 列表的 ✅
- CTA 按钮里的 emoji 箭头(箭头单独出现 OK,emoji 箭头不行)

没图标用真 icon 库(Lucide / Heroicons / Phosphor),或者用 placeholder。

### ❌ SVG 画 imagery

不要试图用 SVG 画:**人物、场景、设备、物品、抽象艺术**。AI 画的 SVG imagery 一眼就是 AI 味,幼稚且廉价。

> 一个灰色矩形 + 「插画位 1200×800」的文字标签,**比一个拙劣的 SVG hero illustration 强 100 倍**。

唯一可以用 SVG 的场景:
- 真正的 icon(16×16 到 32×32 级别)
- 几何图形做装饰元素
- Data viz 的 chart

### ❌ 过多 iconography

不是每个标题 / feature / section 都需要 icon。滥用 icon 会让界面像 toy。Less is more。

### ❌ Glassmorphism / 模糊背景 (DS-4)

任何 `backdrop-filter: blur()` 配半透明白底卡片,或纯模糊的渐变背景,都是 2022 年 AI 设计教程留下的味道。**整个工作流默认禁用**,除非品牌系统明确要求(brand-spec 里写 frosted glass 才能用)。

替代方案:
- 想要"轻盈感" → 用低饱和的纯色背景 + 轻边框
- 想要"高级感" → 用纸张质感、噪点纹理(grain)、或 monochrome + 一颗暖色 accent

### ❌ Inter 当默认字体 (DS-4)

`font-family: Inter, sans-serif` 是 AI 设计的死亡签名——每个 AI 生成的网站、PPT、卡片都在用 Inter。Inter 本身没问题,但作为 **默认值出现在每个产物里**就是 slop。

正确做法:
- **有 brand-spec / 选定 direction**:用 design-system 里指定的 `font_stack.display` + `font_stack.body`(可能是 GT America、Söhne、IBM Plex、Playfair Display 等)
- **没 direction**:用 `system-ui, -apple-system, BlinkMacSystemFont`(平台原生字体,不像 AI)
- **绝对不要**:无脑复制 Inter 作为通用安全选项

### ❌ Design-system anti-patterns 是 P0 (DS-4)

当 system prompt 里出现 `<design-system>` 块时,该块的 **"Anti-patterns" 段落是 P0 级硬规则**——和上面这些通用 slop 同等优先级,不是 9 个段落里的可选条目。

具体执行:
- 选定 modern-minimal:不许出现"卡片左 border accent 色"
- 选定 editorial-magazine:不许 sans-serif display
- 选定 brutalist-experimental:不许圆角 (radius > 0)
- 选定 vintage-film:不许 gradient 背景

违反 = checklist gate 直接 Block,触发 redo。

### ❌ 编造的 "Data slop"

- "10,000+ happy customers"(你都不知道有没有)
- "99.9% uptime"(没有真数据就别写)
- 用图标 + 数字 + 词组成的装饰"metric cards"
- Mock table 里的假数据装点得花里胡哨

**如果没真数据,留 placeholder 或问用户要**。

### ❌ 编造的 "Quote slop"

编造的用户评价、名人名言装饰页面。**留 placeholder 问用户要真 quote**。

## Honest Placeholders 硬规则(M6,P0 级别)

> 这是规则,不是建议。**违反 = 直接重做**。

### 永不编造的字段

下列任意一类的具体数字 / 比例 / 排名,**没有用户提供的来源就不许写**:

- **转化 / 效率类**:"提升 30%"、"节省 40%"、"ROI 5×"、"快 10 倍"
- **用户 / 销量类**:"10 万用户"、"50K customers"、"年销百万"
- **时效声称**:"3 分钟搞定"、"5 秒完成"、"30 天上线"
- **满意度 / 留存**:"90% 满意"、"95% retention"、"NPS 70"
- **市场地位**:"行业第一"、"#1 in market"、"领导者"
- **比较数据**:"比 X 快 10 倍"、"more than competitors"

### 没数据时怎么办

按优先级:

1. **用 `—`(em dash)** 占位
   ```
   转化率: —
   月活跃用户: —
   ```

2. **用带标签的灰块**——视觉上区别于真实内容
   ```html
   <span class="placeholder">[转化率:待补充]</span>
   ```
   ```markdown
   `[转化率:待补充]`
   ```

3. **直接问用户**(在思考阶段输出"我需要 X 数据,你能提供吗"而不是硬填)

### 例外(允许无溯源)

只有两类例外:

- **虚构场景的圆数**——必须明示是 demo / 示例:
  > "假设有 100 个用户使用了这个功能..."

- **行业常识**——必须搜索可验证 + 标注 `(industry avg)`:
  > "电商行业平均转化率 2-3% (industry avg)"

### 模型自检 checklist(emit 前必须过一遍)

emit artifact 前,扫一遍输出里的每个数字 / 百分比:

```
对每个数字 X,问:
  1. 用户在 brief / brand-spec / project context 里提到过 X 吗?
     ├─ 是 → OK
     └─ 否 → 是行业常识吗?
              ├─ 是(可验证)→ 加 (industry avg) 标签
              └─ 否 → 改成 — 或 [...:待补充]
```

任一数字过不了这个检查 → **改完再 emit**。这是 P0,不是 P1。

## 字体陷阱

### ❌ 避免这些烂大街字体

- Inter(AI 生成的网页默认)
- Roboto
- Arial / Helvetica
- 纯 system font stack
- Fraunces(AI 发现了这个就用滥了)
- Space Grotesk(最近 AI 的最爱)

### ✅ 用有特点的 display + body 配对

灵感方向:
- 衬线 display + 无衬线 body(editorial feel)
- Mono display + sans body(technical feel)
- Heavy display + light body(contrast)
- Variable font 做 hero 的粗细动画

字体资源:
- Google Fonts 的冷门好选项(Instrument Serif、Cormorant、Bricolage Grotesque、JetBrains Mono)
- 中文字体不要默认思源黑体——用方正、汉仪、字魂等有性格的

## 色彩陷阱

### ❌ 凭空发明颜色

不要写 `#3b82f6` "因为蓝色看起来专业"。颜色必须有来源:
- 品牌指南给的色值
- 从参考品牌图里采样的色值
- 设计哲学要求的色板(如 Pentagram 的"黑白 + 一个 accent")

### ❌ 颜色过多

UI 中**通常不超过 3-4 种**颜色(主 + 辅 + 强调 + 中性)。"功能性多色"(如 status 标签红 / 黄 / 绿)不算入这个限额。

## 内容陷阱

### ❌ "AI 写作"味道

- 「在当今快节奏的社会中...」/「随着技术的发展...」开头
- 「不仅...而且...」「赋能」「闭环」「打通」「下沉」
- 三段式排比凑字数
- 结尾"让我们一起..."的号召

### ❌ 凑字数

为了填满某个区域硬写文案。**留白 > 烂文案**。

## 在产物里如何检查

交付前对照这份清单**眯眼自检**:
- 有没有 emoji 装饰?
- 有没有 SVG 画的人 / 物?
- 有没有编造的 stats / quotes?
- 字体是不是 Inter / Roboto?
- 颜色是不是凭空选的?
- 文案是不是有「AI 味」?

任一答案是「有」→ **不要交付**,先修掉。
