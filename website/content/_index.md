---
title: DDX Agent
layout: hextra-home
---

{{< hextra/hero-badge link="https://github.com/DocumentDrivenDX/agent" >}}
  {{< icon name="github" attributes="height=14" >}}
  GitHub
{{< /hextra/hero-badge >}}

<div class="hx-mt-6 hx-mb-6">
{{< hextra/hero-headline >}}
  Embeddable Go Agent Runtime
{{< /hextra/hero-headline >}}
</div>

<div class="hx-mb-12">
{{< hextra/hero-subtitle >}}
  Local-model-first via LM Studio. In-process tool-calling LLM loop&nbsp;<br class="sm:hx-block hx-hidden" />for build orchestrators and CI systems.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx-mb-6">
{{< hextra/hero-button text="Get Started" link="docs/getting-started/" >}}
</div>

<div class="hx-mt-6"></div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Embeddable Library"
    subtitle="agent.Run(ctx, request) — no subprocess overhead, direct state sharing with the host."
  >}}
  {{< hextra/feature-card
    title="Local-Model-First"
    subtitle="LM Studio and Ollama support via OpenAI-compatible API. Zero marginal cost for routine tasks."
  >}}
  {{< hextra/feature-card
    title="Full Observability"
    subtitle="Every LLM turn and tool call logged to JSONL. Sessions are replayable and cost-tracked."
  >}}
  {{< hextra/feature-card
    title="Structured Tools"
    subtitle="Shipped tools — read, write, edit, bash, find, grep, ls, patch, task. Simple, auditable, benchmark-ready."
  >}}
  {{< hextra/feature-card
    title="Multi-Provider"
    subtitle="OpenAI-compatible (LM Studio, Ollama, OpenAI, Azure) and Anthropic Claude. One interface."
  >}}
  {{< hextra/feature-card
    title="Standalone CLI"
    subtitle="ddx-agent -p 'prompt' proves the library works. Config, session logs, replay, usage reporting."
  >}}
{{< /hextra/feature-grid >}}
