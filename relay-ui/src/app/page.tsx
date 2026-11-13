"use client";

import React, { useState, useEffect, useRef } from "react";
import Image from "next/image";
import { Terminal, Activity, Send, RefreshCw, Cpu, ShieldAlert, Zap } from "lucide-react";

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

export default function RelayDashboard() {
  const [activeTab, setActiveTab] = useState<"playground" | "metrics">("playground");

  // Playground States
  const [inputPrompt, setInputPrompt] = useState("");
  const [messages, setMessages] = useState<Message[]>([
    {
      id: "sys-init",
      role: "system",
      content: "Connected to the Relay gateway at http://localhost:8080. Ready for requests.",
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

    const userMsg: Message = {
      id: `user-${Date.now()}`,
      role: "user",
      content: inputPrompt.trim(),
      timestamp: new Date().toLocaleTimeString(),
    };

    setMessages((prev) => [...prev, userMsg]);
    setInputPrompt("");
    setIsTransmitting(true);

    // Build the full conversation history matching the standard OpenAI format
    const conversationHistory = [
      ...messages
        .filter((m) => m.role === "user" || m.role === "assistant")
        .map((m) => ({ role: m.role, content: m.content })),
      { role: "user" as const, content: userMsg.content },
    ];

    try {
      const response = await fetch("/api/relay/v1/chat/completions", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          model: selectedModel,
          messages: conversationHistory,
        }),
      });

      if (!response.ok) {
        const errText = await response.text();
        throw new Error(`gateway returned ${response.status}. ${errText}`.trim());
      }

      const data = await response.json();
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
      const res = await fetch("/api/relay/metrics");
      if (!res.ok) {
        throw new Error(
          res.status === 404
            ? "/metrics is not exposed through the console proxy. Open http://localhost:8080/metrics on the gateway host instead."
            : `HTTP ${res.status}: ${res.statusText}`
        );
      }
      const text = await res.text();
      setMetricsData(text);
      setLastMetricsFetch(new Date().toLocaleTimeString());
    } catch (err) {
      setMetricsData(`Metrics unavailable: ${err instanceof Error ? err.message : "Unknown error"}`);
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
          <Image src="/logo-mark.svg" alt="Relay logo" width={40} height={40} className="h-10 w-10 shrink-0" unoptimized priority />
          <div className="grid leading-none">
            <h1 className="font-display text-xl font-bold uppercase tracking-[0.03em] text-white">
              Re<span className="text-[#FF2626]">lay</span>
            </h1>
            <span className="mt-1 font-mono text-[10px] font-bold uppercase tracking-[0.22em] text-[#FF2626]">By TECHMINES</span>
          </div>
          <span className="tm-label ml-3 hidden lg:inline">LLM gateway console</span>
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
                  <option value="gpt-4">gpt-4</option>
                  <option value="llama3">llama3 (ollama)</option>
                  <option value="mock-v1">mock-v1</option>
                </select>
              </label>

              <div className="hidden items-center gap-2 font-mono text-xs text-dim md:flex">
                <span className="h-2 w-2 bg-[#39FF14]" aria-hidden="true"></span>
                <span>POST :8080/v1/chat/completions</span>
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
                        <p className="whitespace-pre-wrap text-sm text-white">{m.content}</p>
                      </div>
                    </div>
                  );
                }

                return (
                  <div key={m.id} className="flex flex-col items-start">
                    <div className="max-w-[85%] border border-line bg-panel p-4">
                      <div className="tm-label mb-2 flex items-center justify-between gap-4 text-[10px]">
                        <span className="flex items-center gap-1.5 text-white">
                          <Cpu className="h-3 w-3" aria-hidden="true" />
                          {m.model}
                        </span>
                        <span>{m.timestamp}</span>
                      </div>
                      <p className="whitespace-pre-wrap text-sm leading-relaxed text-white">{m.content}</p>
                      {m.usage && (
                        <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 border-t border-line pt-2 font-mono text-[10px] text-dim">
                          <span>Prompt {m.usage.prompt_tokens}</span>
                          <span>Completion {m.usage.completion_tokens}</span>
                          <span>Total {m.usage.total_tokens} tokens</span>
                        </div>
                      )}
                    </div>
                  </div>
                );
              })}

              {isTransmitting && (
                <div className="flex items-center gap-3 border border-line bg-panel-2 p-3 font-mono text-xs text-white" role="status">
                  <div className="h-2.5 w-2.5 animate-ping bg-[#FF2626]" aria-hidden="true"></div>
                  <span>Routing request through the gateway...</span>
                </div>
              )}
            </div>

            {/* Prompt input */}
            <form onSubmit={handleTransmit} className="border-t border-line p-3 sm:p-4">
              <div className="flex gap-3">
                <textarea
                  rows={2}
                  value={inputPrompt}
                  onChange={(e) => setInputPrompt(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey) {
                      e.preventDefault();
                      handleTransmit();
                    }
                  }}
                  placeholder="Ask something. Enter sends, Shift+Enter adds a line."
                  aria-label="Prompt"
                  className="flex-1 resize-none border border-line bg-black p-3 text-sm text-white placeholder:text-neutral-500"
                />

                <button type="submit" disabled={isTransmitting || !inputPrompt.trim()} className="tm-btn tm-btn--solid shrink-0">
                  <Send className="h-4 w-4" aria-hidden="true" />
                  <span className="hidden sm:inline">Send</span>
                </button>
              </div>
            </form>
          </section>
        ) : (
          <section className="tm-card flex h-full flex-col" aria-label="Gateway metrics">
            <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line px-4 py-3">
              <div className="flex min-w-0 items-center gap-3">
                <Terminal className="h-5 w-5 shrink-0 text-[#FF2626]" aria-hidden="true" />
                <h2 className="font-display text-lg font-bold uppercase text-white">Prometheus metrics</h2>
                {lastMetricsFetch && <span className="font-mono text-[11px] text-dim">Updated {lastMetricsFetch}</span>}
              </div>

              <button type="button" onClick={fetchMetrics} disabled={isLoadingMetrics} className="tm-btn !px-3 !py-1.5 !text-xs">
                <RefreshCw className={`h-3.5 w-3.5 ${isLoadingMetrics ? "animate-spin" : ""}`} aria-hidden="true" />
                Refresh
              </button>
            </div>

            <div className="flex-1 overflow-auto bg-black p-4 select-text">
              <pre className="whitespace-pre font-mono text-xs leading-5 text-body">{metricsData}</pre>
            </div>
          </section>
        )}
      </main>
    </div>
  );
}
