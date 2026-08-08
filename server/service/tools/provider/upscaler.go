package provider

import "context"

const transitionalUpscalerType = "upscaler_transition"

type transitionalUpscalerProvider struct {
	imageProvider ImageProvider
}

func NewTransitionalUpscalerProvider(imageProvider ImageProvider) UpscalerProvider {
	if imageProvider == nil {
		return nil
	}
	return &transitionalUpscalerProvider{
		imageProvider: imageProvider,
	}
}

func (p *transitionalUpscalerProvider) Name() string {
	return p.imageProvider.Name()
}

func (p *transitionalUpscalerProvider) Type() string {
	return transitionalUpscalerType
}

func (p *transitionalUpscalerProvider) IsEnabled() bool {
	return p.imageProvider != nil && p.imageProvider.IsEnabled()
}

func (p *transitionalUpscalerProvider) Upscale(ctx context.Context, req *UpscaleRequest) (*UpscaleResult, error) {
	if p.imageProvider == nil {
		return &UpscaleResult{
			Success:  false,
			Error:    "Upscaler provider is not initialized",
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
		return &UpscaleResult{
			Success:  false,
			Error:    "empty provider response",
			Provider: p.Name(),
		}, nil
	}

	return &UpscaleResult{
		Success:       res.Success,
		ImageURLs:     append([]string(nil), res.ImageURLs...),
		ImageData:     append([][]byte(nil), res.ImageData...),
		ProviderJobID: res.ProviderJobID,
		Error:         res.Error,
		Duration:      res.Duration,
		Provider:      res.Provider,
	}, nil
}
