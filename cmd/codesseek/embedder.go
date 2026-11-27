package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"codesseek/internal/config"
	"codesseek/internal/embedder"
	"codesseek/internal/store"
)

// buildEmbedder creates the embedder for a model name. The fake hash embedder
// is selected when an index was built with it (used by fixtures and tests).
func buildEmbedder(cfg config.Config, modelName string) (embedder.Embedder, func(), error) {
	if strings.HasPrefix(modelName, "fake-") {
		dim := 8
		if _, err := fmt.Sscanf(modelName, "fake-hash-%d", &dim); err != nil || dim <= 0 {
			dim = 8
		}
		return embedder.NewFake(dim), func() {}, nil
	}
	if err := ensureModelFiles(cfg); err != nil {
		return nil, nil, err
	}
	dllPath, err := ensureRuntimeDLL()
	if err != nil {
		return nil, nil, err
	}
	e, err := embedder.NewONNX(embedder.ONNXOptions{
		ModelDir:  cfg.ModelPath(),
		ModelName: modelName,
		DLLPath:   dllPath,
	})
	if err != nil {
		return nil, nil, err
	}
	return e, func() { _ = e.Close() }, nil
}

// embedderForMeta matches an existing index so a query cannot mix models.
func embedderForMeta(cfg config.Config, meta *store.Meta) (embedder.Embedder, func(), error) {
	if meta.Model != cfg.Model {
		return nil, nil, fmt.Errorf("index was built with %s but current model is %s; set the model back or reindex with --full",
			meta.Model, cfg.Model)
	}
	if strings.HasPrefix(meta.Model, "fake-") {
		return embedder.NewFake(meta.Dim), func() {}, nil
	}
	if meta.Dim != 512 {
		return nil, nil, fmt.Errorf("index dim %d is not supported by %s; reindex with --full", meta.Dim, cfg.Model)
	}
	return buildEmbedder(cfg, meta.Model)
}

// indexEmbedder returns the embedder an index run should use. When an index
// already exists and a full rebuild is not requested, the model is loaded
// lazily: a run that turns out to skip every file never pays the ONNX startup
// cost (~3s on this machine).
func indexEmbedder(cfg config.Config, st *store.Store, full bool) (embedder.Embedder, func(), error) {
	meta, err := st.LoadMeta()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return buildEmbedder(cfg, cfg.Model)
	case err != nil:
		return nil, nil, err
	}
	if full || meta.Dim <= 0 {
		return buildEmbedder(cfg, cfg.Model)
	}
	if meta.Model != cfg.Model {
		return nil, nil, fmt.Errorf("index was built with %s but current model is %s; set the model back or reindex with --full",
			meta.Model, cfg.Model)
	}
	lazy := newLazyEmbedder(meta.Model, meta.Dim, func() (embedder.Embedder, func(), error) {
		return buildEmbedder(cfg, cfg.Model)
	})
	return lazy, func() { _ = lazy.Close() }, nil
}

// lazyEmbedder reports the index's model and dim without loading ONNX and only
// builds the real embedder on the first embedding call.
type lazyEmbedder struct {
	model string
	dim   int
	build func() (embedder.Embedder, func(), error)

	mu     sync.Mutex
	inner  embedder.Embedder
	closer func()
	err    error
}

func newLazyEmbedder(model string, dim int, build func() (embedder.Embedder, func(), error)) *lazyEmbedder {
	return &lazyEmbedder{model: model, dim: dim, build: build}
}

func (l *lazyEmbedder) Model() string { return l.model }
func (l *lazyEmbedder) Dim() int      { return l.dim }

func (l *lazyEmbedder) get() (embedder.Embedder, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	if l.inner != nil {
		return l.inner, nil
	}
	inner, closer, err := l.build()
	if err != nil {
		l.err = err
		return nil, err
	}
	if inner.Model() != l.model || inner.Dim() != l.dim {
		closer()
		l.err = fmt.Errorf("loaded embedder %s/%d does not match the index %s/%d",
			inner.Model(), inner.Dim(), l.model, l.dim)
		return nil, l.err
	}
	l.inner, l.closer = inner, closer
	return inner, nil
}

func (l *lazyEmbedder) EmbedDocuments(texts []string) ([][]float32, error) {
	inner, err := l.get()
	if err != nil {
		return nil, err
	}
	return inner.EmbedDocuments(texts)
}

func (l *lazyEmbedder) EmbedQuery(text string) ([]float32, error) {
	inner, err := l.get()
	if err != nil {
		return nil, err
	}
	return inner.EmbedQuery(text)
}

func (l *lazyEmbedder) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closer != nil {
		l.closer()
		l.closer = nil
	}
	return nil
}
