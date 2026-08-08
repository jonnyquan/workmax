package provider

import "context"

const transitionalBackgroundRemoverType = "background_remover_transition"

type transitionalBackgroundRemoverProvider struct {
	imageProvider ImageProvider
}

func NewTransitionalBackgroundRemoverProvider(imageProvider ImageProvider) BackgroundRemoverProvider {
	if imageProvider == nil {
		return nil
	}
	return &transitionalBackgroundRemoverProvider{
		imageProvider: imageProvider,
	}
}

func (p *transitionalBackgroundRemoverProvider) Name() string {
	return p.imageProvider.Name()
}

func (p *transitionalBackgroundRemoverProvider) Type() string {
	return transitionalBackgroundRemoverType
}

func (p *transitionalBackgroundRemoverProvider) IsEnabled() bool {
	return p.imageProvider != nil && p.imageProvider.IsEnabled()
}

func (p *transitionalBackgroundRemoverProvider) RemoveBackground(ctx context.Context, req *BackgroundRemovalRequest) (*BackgroundRemovalResult, error) {
	if p.imageProvider == nil {
		return &BackgroundRemovalResult{
			Success:  false,
			Error:    "Background remover provider is not initialized",
			Provider: "",
		}, nil
	}

	genReq := &GenerationRequest{
		Model:           req.Model,
		Prompt:          req.Prompt,
		NegativePrompt:  req.NegativePrompt,
		Width:           req.Width,
		Height:          req.Height,
		ReferenceImages: req.ReferenceImages,
		NumImages:       1,
		TaskID:          req.TaskID,
		OnProgress:      req.OnProgress,
		OnProviderJob:   req.OnProviderJob,
	}

	res, err := p.imageProvider.Generate(ctx, genReq)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return &BackgroundRemovalResult{
			Success:  false,
			Error:    "empty provider response",
			Provider: p.Name(),
		}, nil
	}

	return &BackgroundRemovalResult{
		Success:       res.Success,
		ImageURLs:     append([]string(nil), res.ImageURLs...),
		ImageData:     append([][]byte(nil), res.ImageData...),
		ProviderJobID: res.ProviderJobID,
		Error:         res.Error,
		Duration:      res.Duration,
		Provider:      res.Provider,
	}, nil
}
