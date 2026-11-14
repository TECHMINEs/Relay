"use client";

import { useState } from "react";
import Image from "next/image";
import { Activity, ArrowRight, Check, CircleAlert, Play, RotateCcw } from "lucide-react";

type Scenario = "healthy" | "failure";
type Tab = "walkthrough" | "metrics";

type DemoRun = {
  prompt: string;
  scenario: Scenario;
  response: string;
  promptTokens: number;
  completionTokens: number;
  simulatedLatency: number;
  time: string;
};

type DemoCounters = {
  requests: number;
  primaryAttempts: number;
  primaryFailures: number;
  fallbackAttempts: number;
  successes: number;
  estimatedTokens: number;
};

const emptyCounters: DemoCounters = {
  requests: 0,
  primaryAttempts: 0,
  primaryFailures: 0,
  fallbackAttempts: 0,
  successes: 0,
  estimatedTokens: 0,
};

const presets = [
  "Summarize a support ticket",
  "Explain a failed deployment",
  "Draft a concise incident update",
];

const steps = [
  { title: "Request", detail: "A chat completion request enters Relay with a prompt and model choice." },
  { title: "Routing", detail: "Relay chooses a healthy provider. This demo illustrates that decision with a fixed primary." },
  { title: "Provider attempt", detail: "The simulated primary either responds or fails, depending on your selected scenario." },
  { title: "Fallback", detail: "After an injected failure, Relay illustrates a secondary provider attempt." },
  { title: "Metrics", detail: "The browser updates counters for this walkthrough session." },
];

const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

export default function RelayDashboard() {
  const [tab, setTab] = useState<Tab>("walkthrough");
  const [prompt, setPrompt] = useState(presets[0]);
  const [scenario, setScenario] = useState<Scenario>("failure");
  const [stage, setStage] = useState(-1);
  const [running, setRunning] = useState(false);
  const [latestRun, setLatestRun] = useState<DemoRun | null>(null);
  const [counters, setCounters] = useState<DemoCounters>(emptyCounters);
  const traceScenario = latestRun?.scenario ?? scenario;

  const runWalkthrough = async () => {
    const submittedPrompt = prompt.trim();
    if (!submittedPrompt || running) return;
    setRunning(true);
    setLatestRun(null);
    for (let index = 0; index < steps.length; index += 1) {
      setStage(index);
      await pause(index === 2 && scenario === "failure" ? 580 : 360);
    }

    const response = scenario === "failure"
      ? `Scripted fallback response: Relay accepted “${submittedPrompt}” after the simulated primary failed. The secondary provider is illustrative; no model was called.`
      : `Scripted primary response: Relay accepted “${submittedPrompt}” through the simulated primary. No model was called.`;
    const promptTokens = Math.max(1, Math.ceil(submittedPrompt.length / 4));
    const completionTokens = Math.ceil(response.length / 4);
    const run: DemoRun = {
      prompt: submittedPrompt,
      scenario,
      response,
      promptTokens,
      completionTokens,
      simulatedLatency: scenario === "failure" ? 430 : 140,
      time: new Date().toLocaleTimeString(),
    };
    setLatestRun(run);
    setCounters((previous) => ({
      requests: previous.requests + 1,
      primaryAttempts: previous.primaryAttempts + 1,
      primaryFailures: previous.primaryFailures + (scenario === "failure" ? 1 : 0),
      fallbackAttempts: previous.fallbackAttempts + (scenario === "failure" ? 1 : 0),
      successes: previous.successes + 1,
      estimatedTokens: previous.estimatedTokens + promptTokens + completionTokens,
    }));
    setRunning(false);
  };

  const reset = () => {
    if (running) return;
    setStage(-1);
    setLatestRun(null);
    setCounters(emptyCounters);
  };

  const metricText = `# Browser simulation only — not the Relay /metrics endpoint
# Counters reset when this page reloads
relay_demo_requests_total ${counters.requests}
relay_demo_provider_attempts_total{provider="primary"} ${counters.primaryAttempts}
relay_demo_provider_failures_total{provider="primary"} ${counters.primaryFailures}
relay_demo_provider_attempts_total{provider="fallback"} ${counters.fallbackAttempts}
relay_demo_responses_total{result="scripted_success"} ${counters.successes}
relay_demo_estimated_tokens_total ${counters.estimatedTokens}`;

  return (
    <div className="min-h-full text-body">
      <header className="border-b border-line bg-black/85 px-4 py-4 sm:px-6 lg:px-10">
        <div className="mx-auto flex max-w-[1440px] flex-wrap items-center justify-between gap-5">
          <div className="flex items-center gap-3">
            <Image src="/Relay/logo-mark.svg" alt="Relay logo" width={48} height={48} className="h-12 w-12" unoptimized priority />
            <div>
              <h1 className="font-display text-2xl font-bold uppercase leading-none text-white">Re<span className="text-[#FF2626]">lay</span></h1>
              <p className="mt-1 font-mono text-[10px] font-bold uppercase tracking-[0.2em] text-[#FF2626]">By TECHMINES</p>
            </div>
          </div>
          <nav className="flex items-center gap-2" aria-label="Demo sections">
            <button type="button" onClick={() => setTab("walkthrough")} className={`border px-4 py-2 font-display text-sm font-bold uppercase ${tab === "walkthrough" ? "border-[#FF2626] bg-[#FF2626] text-black" : "border-line text-white hover:border-[#FF2626]"}`}>Walkthrough</button>
            <button type="button" onClick={() => setTab("metrics")} className={`border px-4 py-2 font-display text-sm font-bold uppercase ${tab === "metrics" ? "border-[#FF2626] bg-[#FF2626] text-black" : "border-line text-white hover:border-[#FF2626]"}`}>Metrics</button>
          </nav>
        </div>
      </header>

      <main className="mx-auto w-full max-w-[1440px] space-y-7 px-4 py-7 sm:px-6 lg:px-10 lg:py-10">
        <div className="max-w-3xl">
          <p className="tm-label text-[#FF2626]">Interactive browser simulation</p>
          <h2 className="mt-2 font-display text-3xl font-bold leading-tight text-white sm:text-4xl">See a request survive a provider failure.</h2>
          <p className="mt-3 text-sm leading-relaxed text-dim sm:text-base">
            Follow a chat request through routing, a simulated upstream failure, fallback, and session metrics. Both providers and every result here are scripted in your browser. The Go gateway and its API remain in the repository.
          </p>
        </div>

        {tab === "walkthrough" ? (
          <>
            <div className="grid gap-5 lg:grid-cols-[minmax(300px,0.8fr)_minmax(0,1.2fr)]">
              <section className="tm-card p-5 sm:p-6" aria-labelledby="request-heading">
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <p className="tm-label">01 · Configure</p>
                    <h3 id="request-heading" className="mt-1 font-display text-xl font-bold text-white">Sample request</h3>
                  </div>
                  <span className="border border-line px-2 py-1 font-mono text-[10px] uppercase text-dim">POST /v1/chat/completions</span>
                </div>
                <label htmlFor="relay-prompt" className="mt-6 block text-sm font-semibold text-white">Prompt</label>
                <textarea id="relay-prompt" value={prompt} onChange={(event) => setPrompt(event.target.value)} disabled={running} maxLength={300} rows={4} className="mt-2 w-full resize-y border border-line bg-black p-3 font-mono text-sm text-white placeholder:text-dim" placeholder="Enter a short sample prompt" />
                <div className="mt-3 flex flex-wrap gap-2">
                  {presets.map((example) => <button key={example} type="button" onClick={() => setPrompt(example)} disabled={running} className="border border-line px-2 py-1 text-left text-xs text-dim hover:border-[#FF2626] hover:text-white">{example}</button>)}
                </div>
                <fieldset className="mt-6">
                  <legend className="text-sm font-semibold text-white">Provider scenario</legend>
                  <div className="mt-2 grid gap-2 sm:grid-cols-2">
                    <label className={`cursor-pointer border p-3 text-sm ${scenario === "failure" ? "border-[#FF2626] bg-[#FF2626]/10" : "border-line"}`}>
                      <input type="radio" name="scenario" value="failure" checked={scenario === "failure"} onChange={() => setScenario("failure")} disabled={running} className="accent-[#FF2626]" />
                      <span className="ml-2 font-semibold text-white">Primary fails</span>
                      <span className="mt-1 block text-xs text-dim">Inject a timeout; route to fallback.</span>
                    </label>
                    <label className={`cursor-pointer border p-3 text-sm ${scenario === "healthy" ? "border-[#FF2626] bg-[#FF2626]/10" : "border-line"}`}>
                      <input type="radio" name="scenario" value="healthy" checked={scenario === "healthy"} onChange={() => setScenario("healthy")} disabled={running} className="accent-[#FF2626]" />
                      <span className="ml-2 font-semibold text-white">Primary works</span>
                      <span className="mt-1 block text-xs text-dim">Return directly without fallback.</span>
                    </label>
                  </div>
                </fieldset>
                <button type="button" onClick={runWalkthrough} disabled={running || !prompt.trim()} className="action-button mt-6 flex min-h-12 w-full items-center justify-center gap-2 px-5 py-3 font-display text-sm font-bold uppercase text-black disabled:cursor-not-allowed disabled:opacity-50">
                  <Play className="h-4 w-4" aria-hidden="true" />{running ? "Running simulation..." : "Run sample request"}
                </button>
                <p className="mt-3 text-xs text-dim">No prompt leaves this browser. No API key or model is used.</p>
              </section>

              <section className="tm-card p-5 sm:p-6" aria-labelledby="trace-heading">
                <div className="flex items-center justify-between gap-3">
                  <div><p className="tm-label">02 · Follow</p><h3 id="trace-heading" className="mt-1 font-display text-xl font-bold text-white">Request trace</h3></div>
                  <span className="font-mono text-xs text-dim">{running ? "In progress" : latestRun ? "Completed" : "Ready to run"}</span>
                </div>
                <ol className="mt-5 space-y-3">
                  {steps.map((step, index) => {
                    const visited = stage >= index;
                    const current = running && stage === index;
                    const skipped = index === 3 && traceScenario === "healthy" && visited;
                    return (
                      <li key={step.title} className={`grid grid-cols-[2rem_minmax(0,1fr)] gap-3 border p-3 sm:p-4 ${current ? "border-[#FF2626] bg-[#FF2626]/10" : visited ? "border-line bg-panel-2" : "border-line/70 bg-black/40"}`}>
                        <span className={`flex h-8 w-8 items-center justify-center border font-mono text-xs font-bold ${visited ? "border-[#FF2626] text-[#FF2626]" : "border-line text-dim"}`}>{visited && !current ? <Check className="h-4 w-4" /> : String(index + 1).padStart(2, "0")}</span>
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2"><h4 className="font-display text-base font-bold text-white">{step.title}</h4>{skipped && <span className="font-mono text-[10px] uppercase text-dim">Not needed</span>}</div>
                          <p className="mt-1 text-xs leading-relaxed text-dim sm:text-sm">{step.detail}</p>
                          {visited && <p className="mt-2 font-mono text-xs text-white">{index === 0 ? `Prompt: “${latestRun?.prompt || prompt.trim()}”` : index === 1 ? "Route: simulated primary provider" : index === 2 ? traceScenario === "failure" ? "Injected result: primary timeout" : "Simulated primary: scripted success" : index === 3 ? traceScenario === "failure" ? "Simulated fallback: scripted success" : "Primary succeeded; fallback skipped" : "Session counters updated below"}</p>}
                        </div>
                      </li>
                    );
                  })}
                </ol>
              </section>
            </div>

            <section className="grid gap-5 lg:grid-cols-[minmax(0,1.2fr)_minmax(280px,0.8fr)]">
              <div className="tm-card p-5 sm:p-6">
                <p className="tm-label">03 · Result</p>
                <h3 className="mt-1 font-display text-xl font-bold text-white">Scripted response</h3>
                {latestRun ? <>
                  <div className="mt-4 flex flex-wrap items-center gap-2 text-xs"><span className="border border-[#39FF14] px-2 py-1 font-mono uppercase text-[#39FF14]">Delivered</span><span className="text-dim">via simulated {latestRun.scenario === "failure" ? "fallback" : "primary"} · {latestRun.time}</span></div>
                  <p className="mt-4 border-l-[3px] border-[#FF2626] bg-black p-4 text-sm leading-relaxed text-white">{latestRun.response}</p>
                  <p className="mt-3 font-mono text-xs text-dim">Simulated latency: {latestRun.simulatedLatency} ms · estimated tokens: {latestRun.promptTokens + latestRun.completionTokens}</p>
                </> : <p className="mt-4 text-sm text-dim">Run the sample request to reveal its result and see how the selected route changes.</p>}
              </div>
              <div className="tm-card p-5 sm:p-6">
                <p className="tm-label">04 · Observe</p>
                <h3 className="mt-1 font-display text-xl font-bold text-white">Session metrics</h3>
                <div className="mt-4 grid grid-cols-2 gap-3">
                  <Metric label="Requests" value={counters.requests} />
                  <Metric label="Primary failures" value={counters.primaryFailures} />
                  <Metric label="Fallbacks" value={counters.fallbackAttempts} />
                  <Metric label="Scripted successes" value={counters.successes} />
                </div>
                <button type="button" onClick={() => setTab("metrics")} className="mt-5 flex items-center gap-1 text-sm font-bold text-[#FF2626] hover:text-white">Inspect all counters <ArrowRight className="h-4 w-4" /></button>
              </div>
            </section>
          </>
        ) : (
          <section className="tm-card p-5 sm:p-7" aria-labelledby="metrics-heading">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div><p className="tm-label">Browser session</p><h3 id="metrics-heading" className="mt-1 font-display text-2xl font-bold text-white">Simulation metrics</h3><p className="mt-2 max-w-2xl text-sm text-dim">These counters come from the walkthrough on this page. They are not live Prometheus telemetry from the Go gateway and reset on reload.</p></div>
              <button type="button" onClick={reset} disabled={running} className="flex items-center gap-2 border border-line px-3 py-2 text-xs font-bold uppercase text-white hover:border-[#FF2626] disabled:opacity-50"><RotateCcw className="h-4 w-4" /> Reset session</button>
            </div>
            <div className="mt-6 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
              <Metric label="Requests" value={counters.requests} />
              <Metric label="Primary attempts" value={counters.primaryAttempts} />
              <Metric label="Primary failures" value={counters.primaryFailures} />
              <Metric label="Fallback attempts" value={counters.fallbackAttempts} />
              <Metric label="Scripted successes" value={counters.successes} />
              <Metric label="Estimated tokens" value={counters.estimatedTokens} />
            </div>
            <div className="mt-6 flex items-center gap-2 text-sm text-dim"><CircleAlert className="h-4 w-4 shrink-0 text-[#FF2626]" />Run both scenarios to compare the failure and fallback counters.</div>
            <div className="mt-6 border border-line bg-[#050505]">
              <div className="flex items-center gap-2 border-b border-line px-4 py-3 font-mono text-xs uppercase text-dim"><Activity className="h-4 w-4 text-[#FF2626]" /> Prometheus-style session preview</div>
              <pre className="overflow-x-auto p-4 font-mono text-xs leading-6 text-[#39FF14]">{metricText}</pre>
            </div>
          </section>
        )}
      </main>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return <div className="border border-line bg-black/60 p-3"><p className="font-display text-2xl font-bold text-white">{value}</p><p className="mt-1 font-mono text-[10px] uppercase leading-snug tracking-wide text-dim">{label}</p></div>;
}
