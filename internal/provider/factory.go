package provider

type OllamaFactory struct {
	name     string
	endpoint string
}

func NewOllamaFactory(name string, endpoint string) *OllamaFactory {
	return &OllamaFactory{
		name:     name,
		endpoint: endpoint,
	}
}

func (f *OllamaFactory) Name() string { return f.name }

func (f *OllamaFactory) Create(model string, opts Options) Provider {
	return NewOllamaWithTemp(f.name, f.endpoint, model, opts.Temperature)
}

type OpenCodeFactory struct {
	name     string
	endpoint string
	apiKey   string
}

func NewOpenCodeFactory(name string, endpoint, apiKey string) *OpenCodeFactory {
	return &OpenCodeFactory{
		name:     name,
		endpoint: endpoint,
		apiKey:   apiKey,
	}
}

func (f *OpenCodeFactory) Name() string { return f.name }

func (f *OpenCodeFactory) Create(model string, opts Options) Provider {
	return NewOpenCodeWithTemp(f.name, f.endpoint, model, f.apiKey, opts.Temperature)
}

type VLLMFactory struct {
	name     string
	endpoint string
	apiKey   string
}

func NewVLLMFactory(name string, endpoint, apiKey string) *VLLMFactory {
	return &VLLMFactory{
		name:     name,
		endpoint: endpoint,
		apiKey:   apiKey,
	}
}

func (f *VLLMFactory) Name() string { return f.name }

func (f *VLLMFactory) Create(model string, opts Options) Provider {
	return NewVLLMWithTemp(f.name, f.endpoint, model, f.apiKey, opts)
}
