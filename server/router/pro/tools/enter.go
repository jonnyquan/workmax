package tools

import "server/router/pro/tools/workagent"

type RouterGroup struct {
	GeneratorRouter
	VideoGeneratorRouter      VideoGeneratorRouter
	UpscalerRouter            UpscalerRouter
	VectorizerRouter          VectorizerRouter
	AvatarsRouter             AvatarsRouter
	LoraRouter                LoraRouter
	RemoverRouter             RemoverRouter
	CanvasRouter              CanvasRouter
	PromptBuilderRouter       PromptBuilderRouter
	VideoPromptRewriterRouter VideoPromptRewriterRouter
	CharacterRouter           CharacterRouter
	AssetLibraryRouter        AssetLibraryRouter
	TeamRouter                TeamRouter
	GlobalCatalogRouter       GlobalCatalogRouter
	WorkAgentRouter           workagent.RouterGroup
}
