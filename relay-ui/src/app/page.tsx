"use client";

import React, { useState, useEffect, useRef } from "react";
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
      content: "RELAY PROTOCOL INITIALIZED. GATEWAY LINK: HTTP://LOCALHOST:8080. STATUS: READY FOR TRANSMISSION.",
      timestamp: new Date().toLocaleTimeString(),
    },
  ]);
  const [isTransmitting, setIsTransmitting] = useState(false);
  const [selectedModel, setSelectedModel] = useState("gpt-4");
  const chatScrollRef = useRef<HTMLDivElement>(null);

  // Metrics States
  const [metricsData, setMetricsData] = useState<string>("INITIALIZING METRICS TELEMETRY...");
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
        throw new Error(`STATUS ${response.status}: ${errText}`);
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
        content: `TRANSMISSION ERROR // GATEWAY ALERT: ${err instanceof Error ? err.message : "Unknown error"}`,
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
            ? "ENDPOINT RESTRICTED: /metrics is blocked by the frontend proxy for security (accessible internally via :8080/metrics)."
            : `HTTP ${res.status}: ${res.statusText}`
        );
      }
      const text = await res.text();
      setMetricsData(text);
      setLastMetricsFetch(new Date().toLocaleTimeString());
    } catch (err) {
      setMetricsData(`ACCESS RESTRICTED // ${err instanceof Error ? err.message : "Unknown error"}`);
    } finally {
      setIsLoadingMetrics(false);
    }
  };

  useEffect(() => {
    if (activeTab === "metrics") {
      fetchMetrics();
    }
  }, [activeTab]);

  return (
    <div className="flex h-full w-full flex-col bg-black text-neutral-200 font-mono select-none overflow-hidden">
      {/* ─── TOP PLASMA NAVIGATION BAR ─── */}
      <header className="flex h-14 sm:h-16 w-full shrink-0 items-center justify-between border-b-2 border-red-600 bg-black px-4 md:px-6 shadow-[0_4px_20px_rgba(255,0,0,0.3)] z-50">
        <div className="flex items-center gap-4">
          <h1 className="text-xl sm:text-2xl font-black tracking-widest text-red-600 drop-shadow-[0_0_8px_rgba(255,0,0,0.8)]">
            RELAY
          </h1>
          <span className="hidden md:inline-block border border-red-900 bg-red-950/40 px-2 py-0.5 text-xs text-red-400 tracking-wider">
            GATEWAY // PROXY ACTIVE
          </span>
        </div>

        {/* Tab Switcher */}
        <div className="flex h-full items-center">
          <button
            onClick={() => setActiveTab("playground")}
            className={`flex h-full items-center gap-2 border-b-4 px-3 md:px-6 text-xs sm:text-sm font-bold tracking-widest transition-all ${
              activeTab === "playground"
                ? "border-red-600 bg-red-950/20 text-red-500 shadow-[inset_0_-8px_16px_rgba(255,0,0,0.25)]"
                : "border-transparent text-neutral-500 hover:border-red-900 hover:text-neutral-300"
            }`}
          >
            <Zap className="h-4 w-4" />
            <span className="hidden sm:inline">PLAYGROUND</span>
          </button>

          <button
            onClick={() => setActiveTab("metrics")}
            className={`flex h-full items-center gap-2 border-b-4 px-3 md:px-6 text-xs sm:text-sm font-bold tracking-widest transition-all ${
              activeTab === "metrics"
                ? "border-cyan-400 bg-cyan-950/20 text-cyan-400 shadow-[inset_0_-8px_16px_rgba(0,255,255,0.25)]"
                : "border-transparent text-neutral-500 hover:border-cyan-900 hover:text-neutral-300"
            }`}
          >
            <Activity className="h-4 w-4" />
            <span className="hidden sm:inline">SYSTEM METRICS</span>
          </button>
        </div>
      </header>

      {/* ─── TAB CONTENT AREA ─── */}
      <main className="flex-1 min-h-0 overflow-hidden p-3 sm:p-6 bg-black">
        {activeTab === "playground" ? (
          /* PLAYGROUND TAB */
          <div className="flex h-full flex-col border-2 border-red-600/70 bg-black shadow-[0_0_15px_rgba(255,0,0,0.2)]">
            {/* Header info strip */}
            <div className="flex flex-wrap items-center justify-between gap-2 border-b border-red-900/60 bg-red-950/20 px-3 sm:px-4 py-2 text-xs">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 min-w-0">
                <span className="hidden sm:inline text-neutral-400">CHANNEL:</span>
                <span className="hidden sm:inline text-red-500 font-bold">COMPLETIONS-V1</span>
                <span className="hidden sm:inline text-neutral-400">|</span>
                <span className="text-neutral-400">MODEL TARGET:</span>
                <select
                  value={selectedModel}
                  onChange={(e) => setSelectedModel(e.target.value)}
                  className="bg-black border border-red-800 text-red-400 px-2 py-0.5 text-xs outline-none focus:border-red-500 focus:shadow-[0_0_8px_rgba(255,0,0,0.6)]"
                >
                  <option value="gpt-4">gpt-4</option>
                  <option value="llama3">llama3 (ollama)</option>
                  <option value="mock-v1">mock-v1</option>
                </select>
              </div>

              <div className="hidden md:flex items-center gap-2 text-neutral-500">
                <span className="h-2 w-2 rounded-none bg-green-500 shadow-[0_0_6px_#39FF14]"></span>
                <span>ENDPOINT: :8080/v1/chat/completions</span>
              </div>
            </div>

            {/* Chat Messages Log */}
            <div
              ref={chatScrollRef}
              className="flex-1 space-y-4 overflow-y-auto p-4 select-text"
            >
              {messages.map((m) => {
                if (m.role === "system") {
                  return (
                    <div
                      key={m.id}
                      className="border border-red-900/80 bg-red-950/30 p-3 text-xs text-red-400 shadow-[0_0_8px_rgba(255,0,0,0.15)] flex items-start gap-2"
                    >
                      <ShieldAlert className="h-4 w-4 shrink-0 text-red-500 mt-0.5" />
                      <div className="flex-1">
                        <span className="font-bold text-red-500 mr-2" suppressHydrationWarning>[{m.timestamp}] SYSTEM:</span>
                        <span>{m.content}</span>
                      </div>
                    </div>
                  );
                }

                if (m.role === "user") {
                  return (
                    <div key={m.id} className="flex flex-col items-end">
                      <div className="max-w-[75%] border-2 border-red-600 bg-neutral-900 p-3 shadow-[0_0_12px_rgba(255,0,0,0.3)]">
                        <div className="mb-1 flex items-center justify-between gap-4 border-b border-red-900 pb-1 text-[10px] text-red-400 uppercase tracking-wider font-bold">
                          <span>TRANSMITTER // OPERATOR</span>
                          <span>{m.timestamp}</span>
                        </div>
                        <p className="text-sm text-neutral-100 whitespace-pre-wrap">{m.content}</p>
                      </div>
                    </div>
                  );
                }

                // Assistant (Pure Black & Neon Cyan)
                return (
                  <div key={m.id} className="flex flex-col items-start">
                    <div className="max-w-[80%] border-2 border-cyan-500 bg-black p-4 shadow-[0_0_14px_rgba(0,255,255,0.3)]">
                      <div className="mb-2 flex items-center justify-between gap-4 border-b border-cyan-900 pb-1 text-[10px] text-cyan-400 uppercase tracking-widest font-bold">
                        <span className="flex items-center gap-1.5">
                          <Cpu className="h-3 w-3 text-cyan-400" />
                          BACKEND RESPONSE // {m.model}
                        </span>
                        <span>{m.timestamp}</span>
                      </div>
                      <p className="text-sm text-cyan-300 font-sans tracking-wide leading-relaxed whitespace-pre-wrap">
                        {m.content}
                      </p>
                      {m.usage && (
                        <div className="mt-3 pt-2 border-t border-cyan-950 flex gap-4 text-[10px] text-cyan-600">
                          <span>PROMPT: {m.usage.prompt_tokens}</span>
                          <span>COMPLETION: {m.usage.completion_tokens}</span>
                          <span>TOTAL: {m.usage.total_tokens} TOKENS</span>
                        </div>
                      )}
                    </div>
                  </div>
                );
              })}

              {isTransmitting && (
                <div className="flex items-center gap-3 border border-cyan-800 bg-cyan-950/20 p-3 text-xs text-cyan-400 shadow-[0_0_10px_rgba(0,255,255,0.2)]">
                  <div className="h-3 w-3 bg-cyan-400 animate-ping"></div>
                  <span className="font-bold tracking-widest animate-pulse">
                    TRANSMITTING PAYLOAD VIA GO GATEWAY PIPELINE...
                  </span>
                </div>
              )}
            </div>

            {/* Input Prompt Box */}
            <form
              onSubmit={handleTransmit}
              className="border-t-2 border-red-600 bg-neutral-950 p-4"
            >
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
                  placeholder="ENTER PROMPT FOR GATEWAY PIPELINE (PRESS ENTER TO TRANSMIT)..."
                  className="flex-1 resize-none border-2 border-neutral-800 bg-black p-3 text-sm text-red-100 placeholder:text-neutral-700 outline-none transition-all focus:border-red-600 focus:shadow-[0_0_15px_rgba(255,0,0,0.6)]"
                />

                <button
                  type="submit"
                  disabled={isTransmitting || !inputPrompt.trim()}
                  className="group flex flex-col items-center justify-center border-2 border-red-600 bg-red-600 px-4 sm:px-8 text-black font-black tracking-widest transition-all hover:bg-black hover:text-red-500 hover:shadow-[0_0_20px_rgba(255,0,0,0.8)] disabled:border-neutral-800 disabled:bg-neutral-900 disabled:text-neutral-600 disabled:shadow-none"
                >
                  <Send className="h-5 w-5 mb-1 group-hover:translate-x-0.5 group-hover:-translate-y-0.5 transition-transform" />
                  <span className="text-xs">TRANSMIT</span>
                </button>
              </div>
            </form>
          </div>
        ) : (
          /* SYSTEM METRICS TAB */
          <div className="flex h-full flex-col border-2 border-cyan-500/80 bg-black shadow-[0_0_15px_rgba(0,255,255,0.2)]">
            {/* Metrics control banner */}
            <div className="flex items-center justify-between border-b-2 border-cyan-900 bg-neutral-950 px-5 py-3">
              <div className="flex items-center gap-3">
                <Terminal className="h-5 w-5 text-cyan-400" />
                <h2 className="text-sm font-bold tracking-widest text-cyan-400">
                  PROMETHEUS TELEMETRY STREAM (/api/relay/metrics)
                </h2>
                {lastMetricsFetch && (
                  <span className="text-[11px] text-neutral-500">
                    LAST REFRESH: {lastMetricsFetch}
                  </span>
                )}
              </div>

              <button
                onClick={fetchMetrics}
                disabled={isLoadingMetrics}
                className="flex items-center gap-2 border border-cyan-500 bg-cyan-950/30 px-4 py-1.5 text-xs font-bold tracking-wider text-cyan-300 hover:bg-cyan-500 hover:text-black hover:shadow-[0_0_12px_rgba(0,255,255,0.6)] transition-all disabled:opacity-50"
              >
                <RefreshCw className={`h-3.5 w-3.5 ${isLoadingMetrics ? "animate-spin" : ""}`} />
                REFRESH METRICS
              </button>
            </div>

            {/* Monospace Hacker Terminal Pre Tag */}
            <div className="flex-1 overflow-auto bg-black p-4 select-text">
              <pre className="font-mono text-xs leading-5 text-emerald-400 whitespace-pre">
                {metricsData}
              </pre>
            </div>
          </div>
        )}
      </main>

      {/* ─── FOOTER TELEMETRY STATUS ─── */}
      <footer className="hidden sm:flex h-8 w-full shrink-0 items-center justify-between gap-4 border-t border-red-900/60 bg-black px-6 text-[10px] text-neutral-500 tracking-wider">
        <div className="flex items-center gap-4 whitespace-nowrap">
          <span>PORT: 3000 (UI) → 8080 (CORE GATEWAY)</span>
          <span>THEME: SHARP PLASMA</span>
        </div>
        <div className="hidden md:flex items-center gap-2 whitespace-nowrap text-red-600 font-bold">
          <span className="h-1.5 w-1.5 bg-red-600 animate-ping"></span>
          <span>RELAY SECURE PROTOCOL ACTIVE</span>
        </div>
      </footer>
    </div>
  );
}
