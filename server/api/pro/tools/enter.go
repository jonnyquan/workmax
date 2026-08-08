package tools

import "server/api/pro/tools/workagent"

type ToolsApiGroup struct {
	GeneratorApi
	VideoGeneratorApi      VideoGeneratorApi
	UpscalerApi            UpscalerApi
	VectorizerApi          VectorizerApi
	AvatarsApi             AvatarsApi
	LoraApi                LoraApi
	RemoverApi             RemoverApi
	CanvasApi              CanvasApi
	PromptBuilderApi       PromptBuilderApi
	VideoPromptRewriterApi VideoPromptRewriterApi
	VideoPromptConfigApi   VideoPromptConfigApi
	CharacterApi           CharacterApi
	TeamApi                TeamApi
	GlobalCatalogApi       GlobalCatalogApi
	WorkAgentApi           workagent.ApiGroup
}
