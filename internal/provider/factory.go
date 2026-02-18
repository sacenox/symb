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
