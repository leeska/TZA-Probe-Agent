package server

import (
	"strings"
	"testing"

	v2 "github.com/komari-monitor/komari-agent/protocol/v2"
)

func TestParseCarrierRouteTrace(t *testing.T) {
	trace := parseCarrierRouteTrace("traceroute to 202.97.14.1\n 1  192.0.2.1  0.42 ms\n 2  202.97.14.1  12.5 ms", "202.97.14.1")
	if !trace.Reached || len(trace.Hops) != 2 {
		t.Fatalf("trace = %#v, want two reached hops", trace)
	}
	if trace.Hops[1].RTTMs != 12.5 {
		t.Fatalf("last hop RTT = %v, want 12.5", trace.Hops[1].RTTMs)
	}

	v6 := parseCarrierRouteTrace(" 1  2001:db8::1  1.1 ms\n 2  2001:db8::2  8.4 ms", "2001:db8::2")
	if !v6.Reached || len(v6.Hops) != 2 {
		t.Fatalf("IPv6 trace = %#v, want reached", v6)
	}
}

func TestParseNexttraceRawTrace(t *testing.T) {
	output := strings.Join([]string{
		"1|192.0.2.1|gateway|0.42|64512|LAN Address|||||0|0",
		"2|223.120.1.9||8.40|58807|China|Beijing|||China Mobile|0|0",
		"3|198.51.100.8||12.50|9808|China|Beijing|||China Mobile|0|0",
	}, "\n")
	trace := parseNexttraceRawTrace(output, "198.51.100.8")
	if !trace.Reached || len(trace.Hops) != 3 {
		t.Fatalf("trace = %#v, want three reached hops", trace)
	}
	if trace.Hops[1].ASN != "58807" || trace.Hops[1].RTTMs != 8.4 {
		t.Fatalf("CMIN2 hop = %#v, want AS58807 at 8.4ms", trace.Hops[1])
	}
	if got := strings.Join(classifyCarrierRoutePath("mobile", trace.Hops), "->"); got != "CMIN2->CMI" {
		t.Fatalf("route = %q, want CMIN2->CMI", got)
	}
}

func TestNexttraceArgsAreBoundedAndFamilyScoped(t *testing.T) {
	args := strings.Join(routeTraceArgs("nexttrace", "-T", 443, "ipv6", 24, "2001:db8::1"), " ")
	for _, required := range []string{"--raw", "--parallel-requests 1", "--max-attempts 1", "--timeout 1000", "-T", "-p 443", "-6", "-m 24", "2001:db8::1"} {
		if !strings.Contains(args, required) {
			t.Fatalf("args = %q, missing %q", args, required)
		}
	}
}

func TestClassifyCarrierRouteUsesTcpQualityLabels(t *testing.T) {
	tests := []struct {
		name    string
		carrier string
		target  string
		hops    []carrierRouteHop
		want    string
	}{
		{name: "cn2", carrier: "telecom", target: "198.51.100.1", hops: []carrierRouteHop{{IP: "59.43.246.1"}}, want: "CN2GIA"},
		{name: "unicom", carrier: "unicom", target: "198.51.100.1", hops: []carrierRouteHop{{IP: "219.158.3.1"}}, want: "4837"},
		{name: "mobile", carrier: "mobile", target: "198.51.100.1", hops: []carrierRouteHop{{IP: "223.120.2.1"}}, want: "CMI"},
		{name: "cmin2 asn overrides broad mobile prefix", carrier: "mobile", hops: []carrierRouteHop{{IP: "223.120.2.1", ASN: "AS58807"}}, want: "CMIN2"},
		{name: "cmin2 to cmi", carrier: "mobile", hops: []carrierRouteHop{{IP: "223.120.2.1", ASN: "58807"}, {IP: "221.183.1.1", ASN: "9808"}}, want: "CMIN2->CMI"},
		{name: "cmi hop before cmin2 still uses conventional summary", carrier: "mobile", hops: []carrierRouteHop{{IP: "2402:4f00::1", ASN: "58453"}, {IP: "2402:4f00::2", ASN: "58807"}, {IP: "2409:8080::1", ASN: "9808"}}, want: "CMIN2->CMI"},
		{name: "combo", carrier: "unicom", hops: []carrierRouteHop{{IP: "103.214.1.1"}, {IP: "219.158.3.1"}}, want: "10099->4837"},
		{name: "fallback", carrier: "telecom", target: "198.51.100.1", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(classifyCarrierRoutePath(tt.carrier, tt.hops), "->"); got != tt.want {
				t.Fatalf("label = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCarrierRouteTraceMasksAddresses(t *testing.T) {
	trace := publicCarrierRouteTrace([]carrierRouteHop{{Hop: 1, IP: "219.158.3.1", ASN: "58807", RTTMs: 12.5, HasRTT: true}, {Hop: 2}})
	if len(trace) != 2 || trace[0].Address != "219.158.*.*" || trace[0].ASN != "AS58807" || trace[0].Network != "CMIN2" || !trace[1].TimedOut {
		t.Fatalf("masked trace = %#v", trace)
	}
}

func TestCarrierRouteErrorsMaskAddresses(t *testing.T) {
	message := sanitizeCarrierRouteError("trace to 219.158.3.1 via 2408:8000:2:123::1 failed")
	if strings.Contains(message, "219.158.3.1") || strings.Contains(message, "2408:8000:2:123::1") {
		t.Fatalf("error still contains raw address: %q", message)
	}
	if !strings.Contains(message, "219.158.*.*") || !strings.Contains(message, "2408:8000:****") {
		t.Fatalf("error did not preserve masked context: %q", message)
	}
}

func TestCarrierRouteEntryJSONContract(t *testing.T) {
	entry := v2.CarrierRouteEntry{Family: "ipv6", Carrier: "mobile", Target: "2001:db8::1", Status: "unsupported"}
	if entry.Family != "ipv6" || entry.Carrier != "mobile" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
}
