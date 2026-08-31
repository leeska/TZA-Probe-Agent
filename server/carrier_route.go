package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
	"github.com/komari-monitor/komari-agent/ws"
)

const (
	carrierRouteDefaultTimeout = 8 * time.Second
	carrierRouteMaxTimeout     = 15 * time.Second
	carrierRouteDefaultHops    = 24
	carrierRouteMaxHops        = 30
	carrierRouteMaxTargets     = 12
	carrierRouteMaxConcurrency = 6
)

var (
	traceRTTPattern         = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*ms`)
	traceHopPattern         = regexp.MustCompile(`^[0-9]+$`)
	carrierRouteIPv4Pattern = regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}`)
	carrierRouteIPv6Pattern = regexp.MustCompile(`(?i)(?:[0-9a-f]{0,4}:){2,}[0-9a-f]{0,4}`)
	carrierRouteSlots       = make(chan struct{}, carrierRouteMaxConcurrency)
)

type carrierRouteHop struct {
	Hop    int
	IP     string
	RTTMs  float64
	HasRTT bool
}

type carrierRouteTrace struct {
	Hops     []carrierRouteHop
	Reached  bool
	Output   string
	ExitCode int
}

// runCarrierRouteProbe executes a bounded batch selected by Core. The agent
// does not accept arbitrary commands and never invokes a shell.
func runCarrierRouteProbe(conn *ws.SafeConn, params v2.CarrierRouteProbeParams) {
	started := time.Now().UTC()
	result := v2.CarrierRouteProbeResult{
		JobID:     params.JobID,
		Family:    normalizeCarrierRouteFamily(params.Family),
		StartedAt: started,
	}

	if params.JobID == "" {
		result.Error = "missing job_id"
		result.FinishedAt = time.Now().UTC()
		uploadCarrierRouteResult(conn, result)
		return
	}
	if result.Family != "ipv4" && result.Family != "ipv6" {
		result.Error = "unsupported IP family"
		result.FinishedAt = time.Now().UTC()
		uploadCarrierRouteResult(conn, result)
		return
	}
	if len(params.Targets) == 0 {
		result.Error = "no targets"
		result.FinishedAt = time.Now().UTC()
		uploadCarrierRouteResult(conn, result)
		return
	}
	targets := params.Targets
	if len(targets) > carrierRouteMaxTargets {
		targets = targets[:carrierRouteMaxTargets]
	}
	timeout := time.Duration(params.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = carrierRouteDefaultTimeout
	}
	if timeout > carrierRouteMaxTimeout {
		timeout = carrierRouteMaxTimeout
	}
	hops := params.MaxHops
	if hops <= 0 {
		hops = carrierRouteDefaultHops
	}
	if hops > carrierRouteMaxHops {
		hops = carrierRouteMaxHops
	}
	concurrency := params.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	if concurrency > carrierRouteMaxConcurrency {
		concurrency = carrierRouteMaxConcurrency
	}

	entries := make([]v2.CarrierRouteEntry, len(targets))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, target := range targets {
		i, target := i, target
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-time.After(timeout):
				entries[i] = failedCarrierRouteEntry(target, result.Family, "probe concurrency timeout", "timeout")
				return
			}
			defer func() { <-sem }()
			select {
			case carrierRouteSlots <- struct{}{}:
			case <-time.After(timeout):
				entries[i] = failedCarrierRouteEntry(target, result.Family, "agent probe concurrency timeout", "timeout")
				return
			}
			defer func() { <-carrierRouteSlots }()
			entries[i] = probeCarrierRouteTarget(target, result.Family, timeout, hops)
		}()
	}
	wg.Wait()
	result.Results = entries
	result.FinishedAt = time.Now().UTC()
	uploadCarrierRouteResult(conn, result)
}

func normalizeCarrierRouteFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "4", "v4", "ipv4", "tcp4":
		return "ipv4"
	case "6", "v6", "ipv6", "tcp6":
		return "ipv6"
	default:
		return ""
	}
}

func probeCarrierRouteTarget(target v2.CarrierRouteTarget, family string, timeout time.Duration, maxHops int) v2.CarrierRouteEntry {
	entry := v2.CarrierRouteEntry{
		TargetID:  target.ID,
		Region:    target.Region,
		Carrier:   target.Carrier,
		Family:    family,
		Target:    safeCarrierRouteTarget(target.Host),
		Status:    "failed",
		CheckedAt: time.Now().UTC(),
	}
	if strings.TrimSpace(target.Host) == "" {
		entry.Error = "empty target host"
		return entry
	}
	port := target.Port
	if port == 0 {
		port = 80
	}
	if port < 1 || port > 65535 {
		entry.Error = "invalid target port"
		return entry
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	targetIP, err := resolveCarrierRouteTarget(ctx, target.Host, family)
	if err != nil && strings.TrimSpace(target.BackupHost) != "" {
		targetIP, err = resolveCarrierRouteTarget(ctx, target.BackupHost, family)
	}
	if err != nil {
		entry.Error = sanitizeCarrierRouteError(err.Error())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			entry.Status = "timeout"
		}
		return entry
	}
	trace, err := executeCarrierRouteTrace(ctx, targetIP, port, family, maxHops)
	if err != nil {
		entry.Error = sanitizeCarrierRouteError(err.Error())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			entry.Status = "timeout"
		} else if strings.Contains(strings.ToLower(err.Error()), "not found") {
			entry.Status = "unsupported"
		}
		return entry
	}
	// Classification is deliberately local and deterministic. A route probe must
	// still work when the agent cannot reach an external ASN service.
	entry.RoutePath = classifyCarrierRoutePath(target.Carrier, trace.Hops)
	entry.Route = strings.Join(entry.RoutePath, "->")
	if entry.Route == "" {
		entry.Route = "Unknown"
	}
	entry.Trace = publicCarrierRouteTrace(trace.Hops)
	entry.Sent = 1
	if trace.Reached {
		entry.Received = 1
		entry.Status = "ok"
	} else {
		entry.Status = "timeout"
		entry.Error = "target was not reached"
	}
	if trace.Reached && len(trace.Hops) > 0 {
		last := trace.Hops[len(trace.Hops)-1]
		if last.HasRTT {
			latency := last.RTTMs
			entry.LatencyMs = &latency
		}
	}
	loss := float64(0)
	if !trace.Reached {
		loss = 100
	}
	entry.LossPercent = &loss
	return entry
}

func failedCarrierRouteEntry(target v2.CarrierRouteTarget, family, message, status string) v2.CarrierRouteEntry {
	return v2.CarrierRouteEntry{
		TargetID:  target.ID,
		Region:    target.Region,
		Carrier:   target.Carrier,
		Family:    family,
		Target:    safeCarrierRouteTarget(target.Host),
		Status:    status,
		CheckedAt: time.Now().UTC(),
		Error:     sanitizeCarrierRouteError(message),
	}
}

// Errors can contain a resolved destination or hop address. Keep the public
// result useful while ensuring raw addresses never leave the Agent process.
func sanitizeCarrierRouteError(message string) string {
	message = carrierRouteIPv4Pattern.ReplaceAllStringFunc(message, func(value string) string {
		return maskCarrierRouteIP(value)
	})
	return carrierRouteIPv6Pattern.ReplaceAllStringFunc(message, func(value string) string {
		return maskCarrierRouteIP(value)
	})
}

func resolveCarrierRouteTarget(ctx context.Context, host, family string) (string, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if ip := net.ParseIP(host); ip != nil {
		if family == "ipv4" && ip.To4() == nil {
			return "", fmt.Errorf("target is not IPv4")
		}
		if family == "ipv6" && ip.To4() != nil {
			return "", fmt.Errorf("target is not IPv6")
		}
		return ip.String(), nil
	}
	network := "ip4"
	if family == "ipv6" {
		network = "ip6"
	}
	addrs, err := net.DefaultResolver.LookupIP(ctx, network, host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = errors.New("no address found")
		}
		return "", fmt.Errorf("resolve target: %w", err)
	}
	return addrs[0].String(), nil
}

func executeCarrierRouteTrace(ctx context.Context, targetIP string, port int, family string, maxHops int) (carrierRouteTrace, error) {
	command, err := exec.LookPath("traceroute")
	if err != nil {
		return carrierRouteTrace{}, err
	}
	var lastTrace carrierRouteTrace
	var lastErr error
	// TCP is closest to TcpQuality's return-path probe. UDP is a useful
	// unprivileged fallback on hosts where raw TCP traceroute is unavailable.
	for _, probe := range []string{"-T", "-U"} {
		args := []string{"-n", probe, "-p", strconv.Itoa(port), "-q", "1", "-w", "1", "-m", strconv.Itoa(maxHops)}
		if family == "ipv6" {
			args = append(args, "-6")
		}
		args = append(args, targetIP)
		cmd := exec.CommandContext(ctx, command, args...)
		output, runErr := cmd.CombinedOutput()
		trace := parseCarrierRouteTrace(string(output), targetIP)
		trace.Output = string(output)
		if cmd.ProcessState != nil {
			trace.ExitCode = cmd.ProcessState.ExitCode()
		}
		lastTrace, lastErr = trace, runErr
		if len(trace.Hops) > 0 {
			return trace, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		return lastTrace, fmt.Errorf("traceroute: %w", lastErr)
	}
	return lastTrace, errors.New("traceroute returned no hops")
}

func parseCarrierRouteTrace(output, targetIP string) carrierRouteTrace {
	trace := carrierRouteTrace{}
	target := net.ParseIP(strings.Trim(targetIP, "[]"))
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !traceHopPattern.MatchString(fields[0]) {
			continue
		}
		var ip string
		for _, field := range fields[1:] {
			candidate := strings.Trim(field, "()[],:;")
			if parsed := net.ParseIP(candidate); parsed != nil {
				ip = parsed.String()
				break
			}
		}
		hopNumber, _ := strconv.Atoi(fields[0])
		hop := carrierRouteHop{Hop: hopNumber, IP: ip}
		if matches := traceRTTPattern.FindStringSubmatch(line); len(matches) == 2 {
			hop.RTTMs, _ = strconv.ParseFloat(matches[1], 64)
			hop.HasRTT = true
		}
		trace.Hops = append(trace.Hops, hop)
		if ip != "" && target != nil && net.ParseIP(ip).Equal(target) {
			trace.Reached = true
		}
	}
	return trace
}

type carrierRouteEvidence struct {
	ASN     string
	Network string
}

// classifyCarrierRoutePath follows TcpQuality's user-facing convention: keep
// backbone transitions in hop order (for example 10099->4837 and
// CMIN2->CMI). It never infers a default route from the destination carrier.
func classifyCarrierRoutePath(_ string, hops []carrierRouteHop) []string {
	labels := make([]string, 0, 3)
	seen := make(map[string]struct{})
	firstCN2 := -1
	hasCTG := false
	for index, hop := range hops {
		evidence := carrierRouteEvidenceForIP(hop.IP)
		if evidence.Network == "CN2" && firstCN2 < 0 {
			firstCN2 = index
		}
		if evidence.Network == "CTG" {
			hasCTG = true
		}
	}
	if firstCN2 >= 0 {
		label := "CN2GIA"
		for _, hop := range hops[firstCN2+1:] {
			if carrierRouteEvidenceForIP(hop.IP).Network == "163" {
				label = "CN2GT"
				break
			}
		}
		if label == "CN2GIA" && hasCTG {
			label = "CTGGIA"
		}
		labels = append(labels, label)
		seen["CN2"] = struct{}{}
		seen["163"] = struct{}{}
		seen["CTG"] = struct{}{}
	}
	for _, hop := range hops {
		label := carrierRouteEvidenceForIP(hop.IP).Network
		if label == "" || label == "CN2" || label == "CTG" {
			continue
		}
		if _, exists := seen[label]; exists {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	return labels
}

func carrierRouteEvidenceForIP(ip string) carrierRouteEvidence {
	ip = strings.ToLower(strings.TrimSpace(ip))
	switch {
	case strings.HasPrefix(ip, "59.43."), strings.HasPrefix(ip, "2605:9d80:"):
		return carrierRouteEvidence{ASN: "AS4809", Network: "CN2"}
	case hasAnyPrefix(ip, "203.22.182.", "203.22.178.", "203.22.179.", "203.128.224.", "69.194.", "2400:9380:"):
		return carrierRouteEvidence{ASN: "AS23764", Network: "CTG"}
	case hasAnyPrefix(ip, "103.214.", "103.228.68.", "103.239.176.", "118.26.151.", "162.219.32.", "162.219.33.", "162.219.34.", "162.219.35.", "162.219.36.", "162.219.37.", "162.219.38.", "162.219.39.", "162.219.85.", "162.245.124.", "202.77.23.", "203.160.66.", "203.160.75.", "2401:8a00:"):
		return carrierRouteEvidence{ASN: "AS10099", Network: "10099"}
	case hasAnyPrefix(ip, "210.14.", "210.51.", "210.78.", "218.105.", "2408:8120:"):
		return carrierRouteEvidence{ASN: "AS9929", Network: "9929"}
	case hasAnyPrefix(ip, "219.158.", "2408:"):
		return carrierRouteEvidence{ASN: "AS4837", Network: "4837"}
	case strings.HasPrefix(ip, "2402:4f00:f000:"):
		return carrierRouteEvidence{ASN: "AS58807", Network: "CMIN2"}
	case hasAnyPrefix(ip, "223.120.", "223.119."):
		return carrierRouteEvidence{ASN: "AS58453", Network: "CMI"}
	case hasAnyPrefix(ip, "221.183.", "111.24.", "111.13.", "2409:8080:"):
		return carrierRouteEvidence{ASN: "AS9808", Network: "CMI"}
	case hasAnyPrefix(ip, "202.97.", "202.96.", "219.141.", "219.142.", "106.37.", "240e:"):
		return carrierRouteEvidence{ASN: "AS4134", Network: "163"}
	default:
		return carrierRouteEvidence{}
	}
}

func hasAnyPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func publicCarrierRouteTrace(hops []carrierRouteHop) []v2.CarrierRouteTraceHop {
	trace := make([]v2.CarrierRouteTraceHop, 0, len(hops))
	for _, hop := range hops {
		evidence := carrierRouteEvidenceForIP(hop.IP)
		item := v2.CarrierRouteTraceHop{
			Hop:      hop.Hop,
			Address:  maskCarrierRouteIP(hop.IP),
			ASN:      evidence.ASN,
			Network:  evidence.Network,
			TimedOut: hop.IP == "",
		}
		if hop.HasRTT {
			rtt := hop.RTTMs
			item.RTTMs = &rtt
		}
		trace = append(trace, item)
	}
	return trace
}

func maskCarrierRouteIP(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.*.*", v4[0], v4[1])
	}
	segments := strings.Split(ip.String(), ":")
	visible := make([]string, 0, 2)
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		visible = append(visible, segment)
		if len(visible) == 2 {
			break
		}
	}
	if len(visible) == 0 {
		return "****:****::"
	}
	if len(visible) == 1 {
		visible = append(visible, "****")
	}
	return visible[0] + ":" + visible[1] + ":****:****:****:****:****:****"
}

func safeCarrierRouteTarget(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "[]")
	if net.ParseIP(value) != nil {
		return maskCarrierRouteIP(value)
	}
	return value
}

func uploadCarrierRouteResult(conn *ws.SafeConn, result v2.CarrierRouteProbeResult) {
	payload := v2.Request{JSONRPC: v2.Version, Method: v2.MethodAgentCarrierRouteResult, Params: result}
	if conn != nil {
		if err := conn.WriteJSON(payload); err != nil {
			log.Printf("Failed to upload carrier route result over WebSocket: %v", err)
		}
		return
	}
	if err := postV2RPC(payload); err != nil {
		log.Printf("Failed to upload carrier route result over POST: %v", err)
	}
}
