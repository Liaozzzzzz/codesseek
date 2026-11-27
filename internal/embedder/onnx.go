package embedder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

const (
	// DefaultModelName is the model identifier stored in meta.json.
	DefaultModelName = "BAAI/bge-small-zh-v1.5"

	// QueryPrefix is the BGE v1.5 retrieval instruction applied to queries
	// only (documents are embedded bare).
	QueryPrefix = "为这个句子生成表示以用于检索相关文章："

	quantizedModelFile = "model_quantized.onnx"
	fallbackModelFile  = "model.onnx"
	tokenizerFile      = "tokenizer.json"

	defaultMaxLen    = 512
	defaultBatchSize = 8
	// maxBatchTokens keeps one ONNX batch under the spec's 8192-token budget.
	maxBatchTokens = 8192
)

// ONNXOptions configures the bge-small-zh-v1.5 embedder.
type ONNXOptions struct {
	ModelDir  string // directory with model_quantized.onnx + tokenizer.json
	ModelName string // model identifier stored in meta.json, default DefaultModelName
	DLLPath   string // onnxruntime.dll; empty means ONNXRUNTIME_DLL / exe dir / cwd
	MaxLen    int    // tokenizer truncation length, default 512
	BatchSize int    // max texts per ONNX batch, default 8
	Threads   int    // intra-op threads, default min(8, NumCPU-1), at least 2
}

// ONNX is the real sentence embedder (CLS pooling + L2 normalization).
type ONNX struct {
	tk         *tokenizer.Tokenizer
	sess       *ort.DynamicAdvancedSession
	sessOpts   *ort.SessionOptions
	inputNames []string
	outDims    []string
	dim        int
	maxLen     int
	batchSize  int
	modelName  string

	mu        sync.Mutex // ONNX sessions are not safe for concurrent Run
	closeOnce sync.Once
}

// NewONNX loads the tokenizer and creates an ONNX Runtime session.
func NewONNX(opt ONNXOptions) (*ONNX, error) {
	if opt.ModelDir == "" {
		return nil, fmt.Errorf("embedder: model dir is required")
	}
	modelPath, err := findModelFile(opt.ModelDir)
	if err != nil {
		return nil, err
	}
	tokPath := filepath.Join(opt.ModelDir, tokenizerFile)
	if _, err := os.Stat(tokPath); err != nil {
		return nil, fmt.Errorf("embedder: tokenizer.json not found in %s: %w", opt.ModelDir, err)
	}

	maxLen := opt.MaxLen
	if maxLen <= 0 {
		maxLen = defaultMaxLen
	}
	batchSize := opt.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	threads := opt.Threads
	if threads <= 0 {
		threads = defaultThreadCount()
	}

	tk, err := pretrained.FromFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("embedder: load tokenizer %s: %w", tokPath, err)
	}
	tk.WithTruncation(&tokenizer.TruncationParams{MaxLength: maxLen, Strategy: tokenizer.LongestFirst})

	graph, err := inspectONNX(modelPath)
	if err != nil {
		return nil, fmt.Errorf("embedder: inspect %s: %w", modelPath, err)
	}
	if len(graph.inputs) == 0 || len(graph.outputs) == 0 {
		return nil, fmt.Errorf("embedder: %s has no inputs/outputs", modelPath)
	}
	inputNames := make([]string, len(graph.inputs))
	for i, in := range graph.inputs {
		inputNames[i] = in.name
	}
	outputNames := make([]string, len(graph.outputs))
	for i, out := range graph.outputs {
		outputNames[i] = out.name
	}
	outDims := graph.outputs[0].dims
	dim := lastNumericDim(outDims)
	if dim <= 0 {
		dim = hiddenSize(opt.ModelDir)
	}
	if dim <= 0 {
		return nil, fmt.Errorf("embedder: cannot determine embedding dim from %v", outDims)
	}

	dll, err := resolveDLL(opt.DLLPath)
	if err != nil {
		return nil, err
	}
	if err := ensureORT(dll); err != nil {
		return nil, err
	}

	sessOpts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("embedder: create session options: %w", err)
	}
	if err := sessOpts.SetIntraOpNumThreads(threads); err != nil {
		_ = sessOpts.Destroy()
		return nil, fmt.Errorf("embedder: set intra-op threads: %w", err)
	}
	// The CPU arena grows to the largest batch and keeps that memory for the
	// whole process. Indexing a large repo then leaves hundreds of MB resident,
	// so trade a little speed for a predictable footprint.
	if err := sessOpts.SetCpuMemArena(false); err != nil {
		_ = sessOpts.Destroy()
		return nil, fmt.Errorf("embedder: disable cpu mem arena: %w", err)
	}
	if err := sessOpts.SetInterOpNumThreads(1); err != nil {
		_ = sessOpts.Destroy()
		return nil, fmt.Errorf("embedder: set inter-op threads: %w", err)
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, outputNames, sessOpts)
	if err != nil {
		_ = sessOpts.Destroy()
		return nil, fmt.Errorf("embedder: create session (inputs=%v outputs=%v): %w", inputNames, outputNames, err)
	}

	modelName := opt.ModelName
	if modelName == "" {
		modelName = DefaultModelName
	}

	return &ONNX{
		tk:         tk,
		sess:       sess,
		sessOpts:   sessOpts,
		inputNames: inputNames,
		outDims:    outDims,
		dim:        dim,
		maxLen:     maxLen,
		batchSize:  batchSize,
		modelName:  modelName,
	}, nil
}

func (e *ONNX) Dim() int      { return e.dim }
func (e *ONNX) Model() string { return e.modelName }

// Close releases the ONNX session. The process-wide runtime environment stays
// initialized, so another embedder can still be created.
func (e *ONNX) Close() error {
	var err error
	e.closeOnce.Do(func() {
		if e.sess != nil {
			err = e.sess.Destroy()
		}
		if e.sessOpts != nil {
			if e2 := e.sessOpts.Destroy(); err == nil {
				err = e2
			}
		}
	})
	return err
}

// EmbedDocuments embeds bare documents (no BGE query prefix).
func (e *ONNX) EmbedDocuments(texts []string) ([][]float32, error) {
	return e.embed(texts, "")
}

// EmbedQuery embeds a query with the BGE retrieval prefix.
func (e *ONNX) EmbedQuery(text string) ([]float32, error) {
	vecs, err := e.embed([]string{text}, QueryPrefix)
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (e *ONNX) embed(texts []string, prefix string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	encodings := make([]*tokenizer.Encoding, len(texts))
	for i, text := range texts {
		enc, err := e.tk.EncodeSingle(strings.ToLower(prefix+text), true)
		if err != nil {
			return nil, fmt.Errorf("embedder: tokenize text %d: %w", i, err)
		}
		if len(enc.Ids) == 0 {
			return nil, fmt.Errorf("embedder: text %d tokenized to nothing", i)
		}
		encodings[i] = enc
	}

	out := make([][]float32, len(texts))
	for start := 0; start < len(texts); {
		end := start
		tokens := 0
		for end < len(texts) {
			n := len(encodings[end].Ids)
			if end > start && (end-start >= e.batchSize || tokens+n > maxBatchTokens) {
				break
			}
			tokens += n
			end++
		}
		if end == start { // a single encoding larger than the token budget
			end = start + 1
		}
		if err := e.runBatch(encodings[start:end], out[start:end]); err != nil {
			return nil, err
		}
		start = end
	}
	return out, nil
}

func (e *ONNX) runBatch(encodings []*tokenizer.Encoding, dst [][]float32) error {
	batch := len(encodings)
	seq := 0
	for _, enc := range encodings {
		if len(enc.Ids) > seq {
			seq = len(enc.Ids)
		}
	}
	if seq == 0 {
		return fmt.Errorf("embedder: empty token sequence")
	}

	ids := make([]int64, batch*seq)
	mask := make([]int64, batch*seq)
	typeIDs := make([]int64, batch*seq)
	for i, enc := range encodings {
		for j, id := range enc.Ids {
			ids[i*seq+j] = int64(id)
			mask[i*seq+j] = 1
		}
		if len(enc.TypeIds) == len(enc.Ids) {
			for j, id := range enc.TypeIds {
				typeIDs[i*seq+j] = int64(id)
			}
		}
	}

	shape := ort.NewShape(int64(batch), int64(seq))
	inputs := make([]ort.Value, 0, len(e.inputNames))
	defer func() {
		for _, v := range inputs {
			if v != nil {
				_ = v.Destroy()
			}
		}
	}()
	values := [][]int64{ids, mask, typeIDs}
	for i := range e.inputNames {
		var data []int64
		if i < len(values) {
			data = values[i]
		} else {
			data = mask // models with extra inputs: reuse the attention mask
		}
		t, err := ort.NewTensor(shape, data)
		if err != nil {
			return fmt.Errorf("embedder: create input %d: %w", i, err)
		}
		inputs = append(inputs, t)
	}

	dims := make([]int64, len(e.outDims))
	for i, d := range e.outDims {
		switch {
		case i == 0:
			dims[i] = int64(batch)
		case strings.HasPrefix(d, "batch"):
			dims[i] = int64(batch)
		case strings.HasPrefix(d, "sequence"), strings.HasPrefix(d, "seq"):
			dims[i] = int64(seq)
		default:
			n, err := parseDim(d)
			if err != nil {
				if i == len(e.outDims)-1 {
					n = int64(e.dim)
				} else {
					return fmt.Errorf("embedder: output dim %q: %w", d, err)
				}
			}
			dims[i] = n
		}
	}
	outTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(dims...))
	if err != nil {
		return fmt.Errorf("embedder: allocate output %v: %w", dims, err)
	}
	defer func() { _ = outTensor.Destroy() }()

	if err := e.sess.Run(inputs, []ort.Value{outTensor}); err != nil {
		return fmt.Errorf("embedder: session run: %w", err)
	}

	data := outTensor.GetData()
	switch len(dims) {
	case 2: // already pooled: [batch, dim]
		dim := int(dims[1])
		for i := 0; i < batch; i++ {
			dst[i] = l2Normalize(append([]float32(nil), data[i*dim:(i+1)*dim]...))
		}
	case 3: // [batch, seq, dim]: CLS pooling
		dim := int(dims[2])
		for i := 0; i < batch; i++ {
			offset := i * int(dims[1]) * dim
			dst[i] = l2Normalize(append([]float32(nil), data[offset:offset+dim]...))
		}
	default:
		return fmt.Errorf("embedder: unsupported output shape %v", dims)
	}
	return nil
}

func findModelFile(modelDir string) (string, error) {
	for _, name := range []string{quantizedModelFile, fallbackModelFile} {
		path := filepath.Join(modelDir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("embedder: neither %s nor %s found in %s; run `codesseek model download`",
		quantizedModelFile, fallbackModelFile, modelDir)
}

// hiddenSize reads config.json as a fallback when the ONNX output shape uses
// symbolic dimensions.
func hiddenSize(modelDir string) int {
	data, err := os.ReadFile(filepath.Join(modelDir, "config.json"))
	if err != nil {
		return 0
	}
	var cfg struct {
		HiddenSize int `json:"hidden_size"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0
	}
	return cfg.HiddenSize
}

func lastNumericDim(dims []string) int {
	for i := len(dims) - 1; i >= 0; i-- {
		if n, err := parseDim(dims[i]); err == nil && n > 0 {
			return int(n)
		}
	}
	return 0
}

func parseDim(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not a numeric dimension %q", s)
	}
	return n, nil
}

var (
	envOnce sync.Once
	envErr  error
)

func ensureORT(dll string) error {
	envOnce.Do(func() {
		ort.SetSharedLibraryPath(dll)
		if ort.IsInitialized() {
			return
		}
		envErr = ort.InitializeEnvironment()
	})
	if envErr != nil {
		return fmt.Errorf("embedder: initialize onnxruntime from %s: %w", dll, envErr)
	}
	return nil
}

// defaultThreadCount leaves one core for the UI and caps ONNX at 8 threads;
// this machine indexes ~3.5x faster than with the old fixed 2.
func defaultThreadCount() int {
	n := runtime.NumCPU() - 1
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

func resolveDLL(explicit string) (string, error) {
	candidates := []string{explicit, os.Getenv("ONNXRUNTIME_DLL")}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "onnxruntime.dll"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "onnxruntime.dll"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c, nil
			}
			return abs, nil
		}
	}
	return "", fmt.Errorf("embedder: onnxruntime.dll not found; put it next to codesseek.exe or set ONNXRUNTIME_DLL")
}
