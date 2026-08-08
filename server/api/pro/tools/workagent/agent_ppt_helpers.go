package workagent

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// buildPPTUserPrompt decorates the raw user prompt with the structured PPT
// requirements the agent needs to produce a deck. Fields that are blank are
// silently skipped so callers don't need to pre-filter.
func buildPPTUserPrompt(basePrompt string, metadata ChatRequestMetadata, files []FileInfo) string {
	base := strings.TrimSpace(basePrompt)
	if base == "" {
		base = "Please generate a presentation based on the provided information."
	}

	spec := metadata.PPTSpec
	if spec == nil && len(metadata.SourcePriority) == 0 && len(files) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n[Structured PPT Requirements]")

	writeLine := func(key, value string) {
		v := strings.TrimSpace(value)
		if v == "" {
			return
		}
		b.WriteString("\n- ")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(v)
	}

	if spec != nil {
		writeLine("Topic", spec.Topic)
		writeLine("Audience", spec.Audience)
		writeLine("Goal", spec.Goal)
		if spec.SlideCount > 0 {
			writeLine("Slide count", strconv.Itoa(spec.SlideCount))
		}
		writeLine("Language", spec.Language)
		writeLine("Tone", spec.Tone)
		writeLine("Style", spec.Style)
		if len(spec.BrandColors) > 0 {
			writeLine("Brand colors", strings.Join(spec.BrandColors, ", "))
		}
		writeLine("Template reference", spec.TemplateRef)
		writeLine("Output format", spec.OutputFormat)
	}

	if len(metadata.SourcePriority) > 0 {
		writeLine("Source priority", strings.Join(metadata.SourcePriority, " > "))
	}

	if len(files) > 0 {
		b.WriteString("\n- Input files:")
		for _, file := range files {
			fileRole := normalizePPTFileRole(file.FileRole, file.Name, file.Type)
			fileName := strings.TrimSpace(file.Name)
			if fileName == "" {
				fileName = "unnamed_file"
			}
			b.WriteString("\n  - ")
			b.WriteString(fileName)
			b.WriteString(" (role: ")
			b.WriteString(fileRole)
			b.WriteString(")")
		}
	}

	b.WriteString("\n- Output requirement: Generate a .pptx file under ./outputs/ and ensure the final answer references that file explicitly.")
	return b.String()
}

func derivePPTMissingFields(
	userMessage string,
	metadata ChatRequestMetadata,
	files []FileInfo,
) ([]string, string) {
	spec := metadata.PPTSpec

	hasTopic := strings.TrimSpace(userMessage) != ""
	hasAudience := false
	hasSlideCount := false
	hasStyle := false

	if spec != nil {
		if strings.TrimSpace(spec.Topic) != "" {
			hasTopic = true
		}
		hasAudience = strings.TrimSpace(spec.Audience) != ""
		hasSlideCount = spec.SlideCount > 0
		hasStyle = strings.TrimSpace(spec.Style) != "" || strings.TrimSpace(spec.Tone) != "" || strings.TrimSpace(spec.TemplateRef) != ""
	}

	if !hasTopic && len(files) > 0 {
		hasTopic = true // files can provide topic context
	}

	missing := make([]string, 0, 5)
	if !hasTopic {
		missing = append(missing, "topic")
	}
	if !hasAudience {
		missing = append(missing, "audience")
	}
	if !hasSlideCount {
		missing = append(missing, "slideCount")
	}
	if !hasStyle {
		missing = append(missing, "style")
	}

	trimmedUser := strings.TrimSpace(userMessage)
	if len(files) == 0 && len([]rune(trimmedUser)) < 24 {
		missing = append(missing, "supportingMaterials")
	}

	suggested := buildPPTSuggestedPrompt(trimmedUser, spec)
	return missing, suggested
}

func buildPPTSuggestedPrompt(userMessage string, spec *PPTSpec) string {
	topic := strings.TrimSpace(userMessage)
	if spec != nil && strings.TrimSpace(spec.Topic) != "" {
		topic = strings.TrimSpace(spec.Topic)
	}
	if topic == "" {
		topic = "企业年度经营复盘与增长计划"
	}

	audience := "管理层与业务负责人"
	if spec != nil && strings.TrimSpace(spec.Audience) != "" {
		audience = strings.TrimSpace(spec.Audience)
	}

	goal := "用于方案汇报与决策讨论"
	if spec != nil && strings.TrimSpace(spec.Goal) != "" {
		goal = strings.TrimSpace(spec.Goal)
	}

	slideCount := 10
	if spec != nil && spec.SlideCount > 0 {
		slideCount = spec.SlideCount
	}

	style := "简洁商务风"
	if spec != nil && strings.TrimSpace(spec.Style) != "" {
		style = strings.TrimSpace(spec.Style)
	}

	language := "中文"
	if spec != nil && strings.TrimSpace(spec.Language) != "" {
		language = strings.TrimSpace(spec.Language)
	}

	return fmt.Sprintf(
		"请生成一份%d页的PPT，主题是“%s”，面向%s，目标是%s，风格为%s，语言为%s。请输出到 ./outputs/final_presentation.pptx，并包含封面、目录、核心分析、结论与下一步行动。",
		slideCount,
		topic,
		audience,
		goal,
		style,
		language,
	)
}

func normalizePPTFileRole(explicitRole, fileName, fileType string) string {
	role := strings.TrimSpace(strings.ToLower(explicitRole))
	switch role {
	case "brief_doc", "data_source", "brand_asset", "reference_ppt":
		return role
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	mimeType := strings.ToLower(fileType)

	switch {
	case ext == ".ppt" || ext == ".pptx" || strings.Contains(mimeType, "presentation"):
		return "reference_ppt"
	case ext == ".xls" || ext == ".xlsx" || ext == ".csv" || strings.Contains(mimeType, "spreadsheet") || strings.Contains(mimeType, "excel"):
		return "data_source"
	case strings.Contains(mimeType, "image"):
		return "brand_asset"
	default:
		return "brief_doc"
	}
}

func hasGeneratedPPTFile(files []FileOutputInfo) bool {
	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file.Name))
		if ext == ".pptx" || ext == ".ppt" {
			return true
		}
	}
	return false
}
