package agui

func Bool(value bool) *bool {
	return &value
}

func Int(value int) *int {
	return &value
}

type AgentCapabilities struct {
	Identity       *IdentityCapabilities       `json:"identity,omitempty"`
	Transport      *TransportCapabilities      `json:"transport,omitempty"`
	Tools          *ToolsCapabilities          `json:"tools,omitempty"`
	Output         *OutputCapabilities         `json:"output,omitempty"`
	State          *StateCapabilities          `json:"state,omitempty"`
	MultiAgent     *MultiAgentCapabilities     `json:"multiAgent,omitempty"`
	Reasoning      *ReasoningCapabilities      `json:"reasoning,omitempty"`
	Multimodal     *MultimodalCapabilities     `json:"multimodal,omitempty"`
	Execution      *ExecutionCapabilities      `json:"execution,omitempty"`
	HumanInTheLoop *HumanInTheLoopCapabilities `json:"humanInTheLoop,omitempty"`
	Custom         map[string]any              `json:"custom,omitempty"`
}

type IdentityCapabilities struct {
	Name             string         `json:"name,omitempty"`
	Type             string         `json:"type,omitempty"`
	Description      string         `json:"description,omitempty"`
	Version          string         `json:"version,omitempty"`
	Provider         string         `json:"provider,omitempty"`
	DocumentationURL string         `json:"documentationUrl,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type TransportCapabilities struct {
	Streaming         *bool `json:"streaming,omitempty"`
	Websocket         *bool `json:"websocket,omitempty"`
	HTTPBinary        *bool `json:"httpBinary,omitempty"`
	PushNotifications *bool `json:"pushNotifications,omitempty"`
	Resumable         *bool `json:"resumable,omitempty"`
}

type ToolsCapabilities struct {
	Supported      *bool  `json:"supported,omitempty"`
	Items          []Tool `json:"items,omitempty"`
	ParallelCalls  *bool  `json:"parallelCalls,omitempty"`
	ClientProvided *bool  `json:"clientProvided,omitempty"`
}

type OutputCapabilities struct {
	StructuredOutput   *bool    `json:"structuredOutput,omitempty"`
	SupportedMIMETypes []string `json:"supportedMimeTypes,omitempty"`
}

type StateCapabilities struct {
	Snapshots       *bool `json:"snapshots,omitempty"`
	Deltas          *bool `json:"deltas,omitempty"`
	Memory          *bool `json:"memory,omitempty"`
	PersistentState *bool `json:"persistentState,omitempty"`
}

type MultiAgentCapabilities struct {
	Supported  *bool                  `json:"supported,omitempty"`
	Delegation *bool                  `json:"delegation,omitempty"`
	Handoffs   *bool                  `json:"handoffs,omitempty"`
	SubAgents  []SubAgentCapabilities `json:"subAgents,omitempty"`
}

type SubAgentCapabilities struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ReasoningCapabilities struct {
	Supported *bool `json:"supported,omitempty"`
	Streaming *bool `json:"streaming,omitempty"`
	Encrypted *bool `json:"encrypted,omitempty"`
}

type MultimodalCapabilities struct {
	Input  *MultimodalInputCapabilities  `json:"input,omitempty"`
	Output *MultimodalOutputCapabilities `json:"output,omitempty"`
}

type MultimodalInputCapabilities struct {
	Image *bool `json:"image,omitempty"`
	Audio *bool `json:"audio,omitempty"`
	Video *bool `json:"video,omitempty"`
	PDF   *bool `json:"pdf,omitempty"`
	File  *bool `json:"file,omitempty"`
}

type MultimodalOutputCapabilities struct {
	Image *bool `json:"image,omitempty"`
	Audio *bool `json:"audio,omitempty"`
}

type ExecutionCapabilities struct {
	CodeExecution    *bool `json:"codeExecution,omitempty"`
	Sandboxed        *bool `json:"sandboxed,omitempty"`
	MaxIterations    *int  `json:"maxIterations,omitempty"`
	MaxExecutionTime *int  `json:"maxExecutionTime,omitempty"`
}

type HumanInTheLoopCapabilities struct {
	Supported        *bool `json:"supported,omitempty"`
	Approvals        *bool `json:"approvals,omitempty"`
	Interventions    *bool `json:"interventions,omitempty"`
	Feedback         *bool `json:"feedback,omitempty"`
	Interrupts       *bool `json:"interrupts,omitempty"`
	ApproveWithEdits *bool `json:"approveWithEdits,omitempty"`
}
