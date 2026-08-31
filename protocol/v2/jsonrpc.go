package v2

import (
	"encoding/json"
	"time"
)

const (
	Version                       = "2.0"
	MethodAgentReport             = "agent.report"
	MethodAgentBasicInfo          = "agent.basicInfo"
	MethodAgentPingResult         = "agent.pingResult"
	MethodAgentTaskResult         = "agent.taskResult"
	MethodAgentExec               = "agent.exec"
	MethodAgentPing               = "agent.ping"
	MethodAgentMessage            = "agent.message"
	MethodAgentEvent              = "agent.event"
	MethodAgentTerminal           = "agent.terminal.request"
	MethodAgentPull               = "agent.pull"
	MethodAgentFile               = "agent.file"
	MethodAgentFileResult         = "agent.file.result"
	MethodAgentCarrierRouteProbe  = "agent.carrierRouteProbe"
	MethodAgentCarrierRouteResult = "agent.carrierRouteResult"
)

type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type TaskResultParams struct {
	TaskID     string    `json:"task_id"`
	Result     string    `json:"result"`
	ExitCode   int       `json:"exit_code"`
	FinishedAt time.Time `json:"finished_at"`
}

type Event struct {
	ID        string      `json:"id"`
	Method    string      `json:"method"`
	Params    interface{} `json:"params,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	ExpiresAt time.Time   `json:"expires_at"`
}

type EventResult struct {
	Status string  `json:"status,omitempty"`
	Events []Event `json:"events,omitempty"`
}

// CarrierRouteTarget is a bounded, server-selected destination used for a
// carrier route probe. Agents never execute a command supplied by the target.
type CarrierRouteTarget struct {
	ID         string `json:"id"`
	Region     string `json:"region,omitempty"`
	Carrier    string `json:"carrier"`
	Host       string `json:"host"`
	BackupHost string `json:"backup_host,omitempty"`
	Port       int    `json:"port,omitempty"`
}

type CarrierRouteProbeParams struct {
	JobID          string               `json:"job_id"`
	Family         string               `json:"family"`
	Targets        []CarrierRouteTarget `json:"targets"`
	TimeoutMs      int                  `json:"timeout_ms"`
	MaxHops        int                  `json:"max_hops"`
	MaxConcurrency int                  `json:"max_concurrency"`
}

type CarrierRouteEntry struct {
	TargetID    string                 `json:"target_id"`
	Region      string                 `json:"region,omitempty"`
	Carrier     string                 `json:"carrier"`
	Family      string                 `json:"family"`
	Target      string                 `json:"target"`
	Route       string                 `json:"route,omitempty"`
	RoutePath   []string               `json:"route_path,omitempty"`
	Trace       []CarrierRouteTraceHop `json:"trace,omitempty"`
	Status      string                 `json:"status"`
	LatencyMs   *float64               `json:"latency_ms,omitempty"`
	LossPercent *float64               `json:"loss_percent,omitempty"`
	Sent        int                    `json:"sent,omitempty"`
	Received    int                    `json:"received,omitempty"`
	CheckedAt   time.Time              `json:"checked_at"`
	Error       string                 `json:"error,omitempty"`
}

// CarrierRouteTraceHop contains only a masked address. Raw hop addresses are
// used transiently for local classification and are never uploaded.
type CarrierRouteTraceHop struct {
	Hop      int      `json:"hop"`
	Address  string   `json:"address,omitempty"`
	ASN      string   `json:"asn,omitempty"`
	Network  string   `json:"network,omitempty"`
	RTTMs    *float64 `json:"rtt_ms,omitempty"`
	TimedOut bool     `json:"timed_out,omitempty"`
}

type CarrierRouteProbeResult struct {
	JobID      string              `json:"job_id"`
	Family     string              `json:"family"`
	Results    []CarrierRouteEntry `json:"results"`
	StartedAt  time.Time           `json:"started_at"`
	FinishedAt time.Time           `json:"finished_at"`
	Error      string              `json:"error,omitempty"`
}

// FileOperation is metadata-only. File contents travel through the dedicated
// HTTP transfer endpoint rather than through JSON-RPC.
type FileOperation struct {
	UUID      string                 `json:"uuid"`
	RequestID string                 `json:"request_id"`
	Op        string                 `json:"op"`
	Args      map[string]interface{} `json:"args,omitempty"`
}

type FileResult struct {
	UUID      string          `json:"uuid"`
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func NewNotification(method string, params interface{}) []byte {
	payload, _ := json.Marshal(Request{JSONRPC: Version, Method: method, Params: params})
	return payload
}

func NewRequest(id interface{}, method string, params interface{}) []byte {
	payload, _ := json.Marshal(Request{JSONRPC: Version, Method: method, Params: params, ID: id})
	return payload
}

func BuildReportPayload(report []byte) []byte {
	return NewNotification(MethodAgentReport, reportParams{Report: json.RawMessage(report)})
}

func BuildReportRequest(id interface{}, report []byte, ackEventIDs []string) []byte {
	return NewRequest(id, MethodAgentReport, reportParams{Report: json.RawMessage(report), AckEventIDs: ackEventIDs})
}

func BuildBasicInfoPayload(info map[string]interface{}) []byte {
	return NewNotification(MethodAgentBasicInfo, map[string]interface{}{"info": info})
}

type reportParams struct {
	Report      json.RawMessage `json:"report"`
	AckEventIDs []string        `json:"ack_event_ids,omitempty"`
}

func BuildPingResultPayload(taskID uint, pingType string, value int, finishedAt time.Time) interface{} {
	return Request{
		JSONRPC: Version,
		Method:  MethodAgentPingResult,
		Params: map[string]interface{}{
			"task_id":     taskID,
			"ping_type":   pingType,
			"value":       value,
			"finished_at": finishedAt.Format(time.RFC3339Nano),
		},
	}
}

func BindParams(raw interface{}, target interface{}) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

func BindResult(raw interface{}, target interface{}) error {
	return BindParams(raw, target)
}
