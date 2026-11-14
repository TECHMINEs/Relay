"use client";

import React, { useState, useEffect, useRef } from "react";
import Image from "next/image";
import { Activity, Send, RefreshCw, ShieldAlert, Zap } from "lucide-react";

interface GatewayResponse {
  id?: string;
  model?: string;
  choices?: Array<{ message?: { content?: string } }>;
  usage?: {
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
  };
}

interface Message {
  id: string;
  role: "user" | "assistant" | "system";
  content: string;
  timestamp: string;
  model?: string;
  usage?: {
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
  };
}

// In-memory telemetry registry for live Prometheus metrics
const telemetry = {
  requestsTotal: 84,
  tokensPrompt: 4120,
  tokensCompletion: 8960,
  modelCounts: {
    "gpt-4": 42,
    "llama3": 26,
    "mock-v1": 16,
  } as Record<string, number>,
  statusCounts: { "200": 82, "429": 2 } as Record<string, number>,
  latencies: [0.12, 0.18, 0.24, 0.31, 0.19, 0.28, 0.45],
};

function generatePrometheusMetrics(): string {
  const totalTokens = telemetry.tokensPrompt + telemetry.tokensCompletion;
  return `# HELP relay_gateway_info Build and gateway version metadata
# TYPE relay_gateway_info gauge
relay_gateway_info{version="1.4.0",commit="3273483",go_version="1.22",runtime="browser-demo"} 1

# HELP relay_requests_total Total number of HTTP requests processed by Relay gateway
# TYPE relay_requests_total counter
relay_requests_total{model="gpt-4",provider="openai",status="200"} ${telemetry.modelCounts["gpt-4"] || 42}
relay_requests_total{model="llama3",provider="ollama",status="200"} ${telemetry.modelCounts["llama3"] || 26}
relay_requests_total{model="mock-v1",provider="mock",status="200"} ${telemetry.modelCounts["mock-v1"] || 16}

# HELP relay_tokens_total Total tokens processed across all upstream providers
# TYPE relay_tokens_total counter
relay_tokens_total{type="prompt"} ${telemetry.tokensPrompt}
relay_tokens_total{type="completion"} ${telemetry.tokensCompletion}
relay_tokens_total{type="total"} ${totalTokens}

# HELP relay_request_duration_seconds Latency of routed LLM requests in seconds
# TYPE relay_request_duration_seconds histogram
relay_request_duration_seconds_bucket{le="0.1"} ${Math.round(telemetry.requestsTotal * 0.15)}
relay_request_duration_seconds_bucket{le="0.25"} ${Math.round(telemetry.requestsTotal * 0.65)}
relay_request_duration_seconds_bucket{le="0.5"} ${Math.round(telemetry.requestsTotal * 0.92)}
relay_request_duration_seconds_bucket{le="1.0"} ${telemetry.requestsTotal}
relay_request_duration_seconds_bucket{le="+Inf"} ${telemetry.requestsTotal}
relay_request_duration_seconds_sum ${ (telemetry.requestsTotal * 0.23).toFixed(2) }
relay_request_duration_seconds_count ${telemetry.requestsTotal}

# HELP relay_cache_hits_total Semantic cache hits for repeated prompt embeddings
# TYPE relay_cache_hits_total counter
relay_cache_hits_total{cache="redis_vector"} 19

# HELP relay_circuit_breaker_state Current circuit breaker status (0=closed/healthy, 1=open)
# TYPE relay_circuit_breaker_state gauge
relay_circuit_breaker_state{provider="openai"} 0
relay_circuit_breaker_state{provider="ollama"} 0
relay_circuit_breaker_state{provider="mock"} 0
`;
}

function simulateGatewayResponse(model: string, userPrompt: string) {
  const promptLower = userPrompt.toLowerCase();
  let text = "";

  if (promptLower.includes("health") || promptLower.includes("status") || promptLower.includes("ping")) {
    text = `[Relay Gateway Status: HEALTHY]\n- Active upstream providers: OpenAI (gpt-4), Local Ollama (llama3), Mock-v1\n- Gateway latency: 22ms round-trip\n- Circuit breaker: CLOSED (0 trip events in last 24h)\n- Rate-limit capacity: 98.4% available`;
  } else if (promptLower.includes("cache") || promptLower.includes("redis")) {
    text = `[Relay Semantic Cache Analysis]\n- Query vector calculated via cosine similarity against cache index.\n- Cache status: MISS (novel input prompt; forward payload to upstream ${model}).\n- TTL applied: 3600 seconds. Future identical semantic queries will bypass LLM inference.`;
  } else if (promptLower.includes("route") || promptLower.includes("load") || promptLower.includes("balance")) {
    text = `[Relay Load Routing Engine]\n- Provider selected: ${model === 'llama3' ? 'Ollama (Local private compute)' : model === 'gpt-4' ? 'OpenAI tier-4 gateway pool' : 'Mock benchmark adapter'}\n- Failover route configured: Primary -> Secondary -> Local fallback\n- Max retry threshold: 2 attempts with exponential backoff.`;
  } else {
    text = `[Relay Router: ${model}]\nProcessed prompt through Relay intelligent gateway pipeline:\n1. Input sanitized & rate-limit token bucket checked (200 OK).\n2. Provider "${model}" dispatched with streaming latency 210ms.\n3. Output telemetry recorded to Prometheus metrics.\n\nResponse: "${userPrompt}" was successfully routed and evaluated. Total pipeline latency remained within p95 SLA targets (<350ms).`;
  }

  const promptTokens = Math.max(8, Math.round(userPrompt.length / 4));
  const completionTokens = Math.max(16, Math.round(text.length / 4));

  // update metrics
  telemetry.requestsTotal += 1;
  telemetry.tokensPrompt += promptTokens;
  telemetry.tokensCompletion += completionTokens;
  telemetry.modelCounts[model] = (telemetry.modelCounts[model] || 0) + 1;

  return {
    id: `relay-${Date.now()}-${Math.random().toString(36).substring(2, 7)}`,
    model,
    text,
    usage: {
      prompt_tokens: promptTokens,
      completion_tokens: completionTokens,
      total_tokens: promptTokens + completionTokens,
    },
  };
}

export default function RelayDashboard() {
  const [activeTab, setActiveTab] = useState<"playground" | "metrics">("playground");

  // Playground States
  const [inputPrompt, setInputPrompt] = useState("");
  const [messages, setMessages] = useState<Message[]>([
    {
      id: "sys-init",
      role: "system",
      content: "Connected to Relay gateway simulator. Running client-side interactive routing demo.",
      timestamp: new Date().toLocaleTimeString(),
    },
  ]);
  const [isTransmitting, setIsTransmitting] = useState(false);
  const [selectedModel, setSelectedModel] = useState("gpt-4");
  const chatScrollRef = useRef<HTMLDivElement>(null);

  // Metrics States
  const [metricsData, setMetricsData] = useState<string>("Loading metrics...");
  const [isLoadingMetrics, setIsLoadingMetrics] = useState(false);
  const [lastMetricsFetch, setLastMetricsFetch] = useState<string>("");

  useEffect(() => {
    if (chatScrollRef.current) {
      chatScrollRef.current.scrollTop = chatScrollRef.current.scrollHeight;
    }
  }, [messages, isTransmitting]);

  const handleTransmit = async (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (!inputPrompt.trim() || isTransmitting) return;

    const userPrompt = inputPrompt.trim();
    const userMsg: Message = {
      id: `user-${Date.now()}`,
      role: "user",
      content: userPrompt,
      timestamp: new Date().toLocaleTimeString(),
    };

    setMessages((prev) => [...prev, userMsg]);
    setInputPrompt("");
    setIsTransmitting(true);

    const conversationHistory = [
      ...messages
        .filter((m) => m.role === "user" || m.role === "assistant")
        .map((m) => ({ role: m.role, content: m.content })),
      { role: "user" as const, content: userMsg.content },
    ];

    try {
      // First attempt real backend if running with live gateway
      let data: GatewayResponse | null = null;
      try {
        const response = await fetch("/api/relay/v1/chat/completions", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ model: selectedModel, messages: conversationHistory }),
        });
        if (response.ok) {
          data = await response.json();
        }
      } catch {
        // network error / static demo fallback
      }

      if (!data) {
        // Simulated gateway delay
        await new Promise((r) => setTimeout(r, 400));
        const sim = simulateGatewayResponse(selectedModel, userPrompt);
        data = {
          id: sim.id,
          model: sim.model,
          choices: [{ message: { content: sim.text } }],
          usage: sim.usage,
        };
      }

      const assistantContent =
        data.choices?.[0]?.message?.content || JSON.stringify(data, null, 2);

      const botMsg: Message = {
        id: data.id || `bot-${Date.now()}`,
        role: "assistant",
        content: assistantContent,
        timestamp: new Date().toLocaleTimeString(),
        model: data.model || selectedModel,
        usage: data.usage,
      };

      setMessages((prev) => [...prev, botMsg]);
    } catch (err) {
      const errorMsg: Message = {
        id: `err-${Date.now()}`,
        role: "system",
        content: `Request failed: ${err instanceof Error ? err.message : "Unknown error"}`,
        timestamp: new Date().toLocaleTimeString(),
      };
      setMessages((prev) => [...prev, errorMsg]);
    } finally {
      setIsTransmitting(false);
    }
  };

  const fetchMetrics = async () => {
    setIsLoadingMetrics(true);
    try {
      let text = "";
      try {
        const res = await fetch("/api/relay/metrics");
        if (res.ok) text = await res.text();
      } catch {
        // fallback to live client generator
      }
      if (!text) {
        text = generatePrometheusMetrics();
      }
      setMetricsData(text);
      setLastMetricsFetch(new Date().toLocaleTimeString());
    } catch {
      setMetricsData(generatePrometheusMetrics());
    } finally {
      setIsLoadingMetrics(false);
    }
  };

  useEffect(() => {
    if (activeTab === "metrics") {
      fetchMetrics();
    }
  }, [activeTab]);

  const tabClass = (tab: "playground" | "metrics") =>
    `relative flex h-full items-center gap-2 px-3 md:px-4 font-display text-sm font-semibold uppercase tracking-[0.06em] transition-colors after:absolute after:left-3 after:right-3 md:after:left-4 md:after:right-4 after:bottom-2 after:h-[3px] after:bg-[#FF2626] after:origin-left after:transition-transform after:duration-200 ${
      activeTab === tab ? "text-white after:scale-x-100" : "text-body hover:text-white after:scale-x-0 hover:after:scale-x-100"
    }`;

  return (
    <div className="flex h-full w-full flex-col overflow-hidden text-body">
      {/* Product header: Relay mark, wordmark and section tabs */}
      <header className="flex h-16 w-full shrink-0 items-center justify-between gap-3 border-b border-line bg-black/80 px-4 md:px-6 backdrop-blur-xl">
        <div className="flex min-w-0 items-center gap-3">
          <Image src="/Relay/logo-mark.svg" alt="Relay logo" width={40} height={40} className="h-10 w-10 shrink-0" unoptimized priority />
          <div className="grid leading-none">
            <h1 className="font-display text-xl font-bold uppercase tracking-[0.03em] text-white">
              Re<span className="text-[#FF2626]">lay</span>
            </h1>
            <span className="mt-1 font-mono text-[10px] font-bold uppercase tracking-[0.22em] text-[#FF2626]">By TECHMINES</span>
          </div>
          <span className="hidden sm:inline-block px-2 py-0.5 text-[10px] font-mono uppercase tracking-wider bg-[rgba(255,38,38,0.15)] text-[#FF2626] border border-[rgba(255,38,38,0.3)] rounded ml-3">
            Interactive Browser Demo
          </span>
        </div>

        <nav className="flex h-full items-center" aria-label="Sections">
          <button type="button" onClick={() => setActiveTab("playground")} className={tabClass("playground")} aria-current={activeTab === "playground"}>
            <Zap className="h-4 w-4" aria-hidden="true" />
            <span className="hidden sm:inline">Playground</span>
          </button>
          <button type="button" onClick={() => setActiveTab("metrics")} className={tabClass("metrics")} aria-current={activeTab === "metrics"}>
            <Activity className="h-4 w-4" aria-hidden="true" />
            <span className="hidden sm:inline">Metrics</span>
          </button>
        </nav>
      </header>

      <main className="flex-1 min-h-0 overflow-hidden p-3 sm:p-6">
        {activeTab === "playground" ? (
          <section className="tm-card flex h-full flex-col" aria-label="Chat playground">
            {/* Model picker and endpoint */}
            <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line px-3 py-2.5 sm:px-4">
              <label className="flex min-w-0 items-center gap-3">
                <span className="tm-label">Model</span>
                <select
                  value={selectedModel}
                  onChange={(e) => setSelectedModel(e.target.value)}
                  className="border border-line bg-black px-2 py-1 font-mono text-xs text-white"
                >
                  <option value="gpt-4">gpt-4 (OpenAI)</option>
                  <option value="llama3">llama3 (Local Ollama)</option>
                  <option value="mock-v1">mock-v1 (Zero Latency)</option>
                </select>
              </label>

              <div className="hidden items-center gap-2 font-mono text-xs text-dim md:flex">
                <span className="h-2 w-2 bg-[#39FF14]" aria-hidden="true"></span>
                <span>Client Simulated Gateway • Interactive Demo</span>
              </div>
            </div>

            {/* Conversation */}
            <div ref={chatScrollRef} className="flex-1 space-y-4 overflow-y-auto p-4 select-text">
              {messages.map((m) => {
                if (m.role === "system") {
                  return (
                    <div key={m.id} className="flex items-start gap-2 border border-line border-l-[3px] border-l-[#FF2626] bg-panel-2 p-3 font-mono text-xs text-body">
                      <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-[#FF2626]" aria-hidden="true" />
                      <div className="flex-1">
                        <span className="mr-2 font-bold text-white" suppressHydrationWarning>[{m.timestamp}] System</span>
                        <span>{m.content}</span>
                      </div>
                    </div>
                  );
                }

                if (m.role === "user") {
                  return (
                    <div key={m.id} className="flex flex-col items-end">
                      <div className="max-w-[80%] border border-[#FF2626] bg-black p-3">
                        <div className="tm-label mb-1.5 flex items-center justify-between gap-4 text-[10px]">
                          <span className="text-[#FF2626]">You</span>
                          <span>{m.timestamp}</span>
                        </div>
                        <p className="whitespace-pre-wrap font-mono text-xs text-white">{m.content}</p>
                      </div>
                    </div>
                  );
                }

                return (
                  <div key={m.id} className="flex flex-col items-start">
                    <div className="max-w-[85%] border border-line bg-panel p-3">
                      <div className="tm-label mb-1.5 flex items-center justify-between gap-4 text-[10px]">
                        <span className="font-bold text-white">{m.model || "Assistant"}</span>
                        <span>{m.timestamp}</span>
                      </div>
                      <p className="whitespace-pre-wrap font-mono text-xs text-body leading-relaxed">{m.content}</p>
                      {m.usage && (
                        <div className="mt-2.5 flex flex-wrap gap-3 border-t border-line pt-1.5 font-mono text-[10px] text-dim">
                          <span>Prompt: {m.usage.prompt_tokens} tokens</span>
                          <span>Completion: {m.usage.completion_tokens} tokens</span>
                          <span>Total: {m.usage.total_tokens} tokens</span>
                        </div>
                      )}
                    </div>
                  </div>
                );
              })}
              {isTransmitting && (
                <div className="flex items-center gap-2 border border-line bg-panel p-3 font-mono text-xs text-dim">
                  <RefreshCw className="h-3.5 w-3.5 animate-spin text-[#FF2626]" />
                  <span>Relay gateway routing to {selectedModel}...</span>
                </div>
              )}
            </div>

            {/* Input Bar */}
            <form onSubmit={handleTransmit} className="border-t border-line p-3 sm:p-4">
              <div className="flex gap-2">
                <input
                  type="text"
                  value={inputPrompt}
                  onChange={(e) => setInputPrompt(e.target.value)}
                  placeholder="Test a prompt, check status, or simulate model routing..."
                  className="flex-1 border border-line bg-black px-3 py-2 font-mono text-xs text-white placeholder-dim focus:border-[#FF2626] focus:outline-none"
                  disabled={isTransmitting}
                />
                <button
                  type="submit"
                  disabled={isTransmitting || !inputPrompt.trim()}
                  className="action-button flex items-center gap-1.5 px-4 py-2 font-mono text-xs font-bold uppercase tracking-wider text-black disabled:opacity-50"
                >
                  <Send className="h-3.5 w-3.5" />
                  <span>Transmit</span>
                </button>
              </div>
            </form>
          </section>
        ) : (
          <section className="tm-card flex h-full flex-col" aria-label="Metrics inspector">
            <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
              <div className="flex items-center gap-2 font-mono text-xs text-dim">
                <Activity className="h-4 w-4 text-[#FF2626]" />
                <span>Prometheus Metrics Exposition</span>
                {lastMetricsFetch && <span className="text-[10px]">({lastMetricsFetch})</span>}
              </div>
              <button
                type="button"
                onClick={fetchMetrics}
                disabled={isLoadingMetrics}
                className="flex items-center gap-1.5 border border-line bg-panel px-3 py-1 font-mono text-xs text-white hover:border-[#FF2626] transition-colors"
              >
                <RefreshCw className={`h-3 w-3 ${isLoadingMetrics ? "animate-spin text-[#FF2626]" : ""}`} />
                <span>Refresh</span>
              </button>
            </div>
            <pre className="flex-1 overflow-auto p-4 font-mono text-xs leading-5 text-[#39FF14]/90 bg-[#050505]">
              {metricsData}
            </pre>
          </section>
        )}
      </main>
    </div>
  );
}
