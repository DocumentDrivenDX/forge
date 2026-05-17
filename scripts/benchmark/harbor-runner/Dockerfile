# fizeau-harbor-runner: wraps Harbor + Fizeau's Python adapters so the host
# stops needing a Python toolchain to drive bench runs.
#
# Resolved pin: harbor==0.3.0 from PyPI. The bead description mentioned
# "harbor-bench" but no such package is published; the Harbor CLI ships as
# `harbor` on PyPI, and Fizeau's harbor_agent.py already targets the 0.3.x
# BaseInstalledAgent API (see scripts/benchmark/harbor_agent.py:3).
FROM python:3.12-slim

RUN pip install --no-cache-dir harbor==0.3.0

COPY scripts/benchmark/harbor_adapters/ /app/harbor_adapters/
COPY scripts/benchmark/harbor_agent.py /app/harbor_agent.py

ENV PYTHONPATH=/app
WORKDIR /app

ENTRYPOINT ["harbor"]
